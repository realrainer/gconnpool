package gconnpool

import (
	"context"
	"fmt"
	"sync"
	"time"
)

var (
	ErrPoolClosed = fmt.Errorf("pool closed")
	ErrPoolFull   = fmt.Errorf("pool is full. put without get ?")
)

type FactoryFuncType[T any] func() (T, error)
type CloseFuncType[T any] func(T) error

type Config struct {
	// initial connections count
	Initial int
	// max connections count
	Max int
	// max idle connections count
	MaxIdle int
	// idle connection timeout
	IdleTimeout time.Duration
	// timeout precision
	TimeoutPrecision time.Duration
}

type idleConnWrap[T any] struct {
	conn         T
	lastActiveAt time.Time
}

type Pool[T any] struct {
	wg               sync.WaitGroup
	workerMutex      sync.Mutex
	done             chan struct{}
	idleConns        chan idleConnWrap[T]
	slots            chan struct{}
	factoryFunc      FactoryFuncType[T]
	closeFunc        CloseFuncType[T]
	max              int
	maxIdle          int
	idleTimeout      time.Duration
	timeoutPrecision time.Duration
}

// Create a new connection pool
func New[T any](config *Config, factoryFunc FactoryFuncType[T], closeFunc CloseFuncType[T]) (*Pool[T], error) {
	if config == nil {
		return nil, fmt.Errorf("no config given")
	}
	if factoryFunc == nil {
		return nil, fmt.Errorf("factory function must be defined")
	}
	if closeFunc == nil {
		return nil, fmt.Errorf("close function must be defined")
	}
	if config.Initial > config.MaxIdle || config.Initial > config.Max || config.MaxIdle > config.Max {
		return nil, fmt.Errorf("invalid configuration")
	}
	p := Pool[T]{
		done:        make(chan struct{}),
		idleConns:   make(chan idleConnWrap[T], config.MaxIdle),
		slots:       make(chan struct{}, config.Max),
		factoryFunc: factoryFunc,
		closeFunc:   closeFunc,
		max:         config.Max,
		maxIdle:     config.MaxIdle,
		idleTimeout: config.IdleTimeout,
	}
	if config.TimeoutPrecision == 0 {
		p.timeoutPrecision = time.Second * 10
	} else {
		p.timeoutPrecision = config.TimeoutPrecision
	}
	for i := 0; i < p.max; i++ {
		p.slots <- struct{}{}
	}
	for i := 0; i < config.Initial; i++ {
		<-p.slots
		if conn, err := p.factoryFunc(); err != nil {
			p.ReleaseIdle()
			return nil, fmt.Errorf("can't create initial connection pool: %w", err)
		} else {
			p.idleConns <- idleConnWrap[T]{
				conn:         conn,
				lastActiveAt: time.Now(),
			}
		}
	}
	p.startWorker()
	return &p, nil
}

// Gets a connection from pool.
// If no idle connections available - creates new one.
// Function blocks until idle connection not available or connections count greater or equal max connections
func (p *Pool[T]) Get() (T, error) {
	return p.GetWithContext(context.Background())
}

// Gets a connection from pool with context
func (p *Pool[T]) GetWithContext(ctx context.Context) (T, error) {
	p.workerMutex.Lock()
	defer p.workerMutex.Unlock()

	p.wg.Add(1)
	defer p.wg.Done()

	var z T

	select {
	case <-ctx.Done():
		return z, ctx.Err()
	case <-p.done:
		return z, ErrPoolClosed
	default:
	}

	for {
		select {
		case <-ctx.Done():
			return z, ctx.Err()
		case <-p.done:
			return z, ErrPoolClosed
		case c := <-p.idleConns:
			return c.conn, nil
		default:
			select {
			case <-ctx.Done():
				return z, ctx.Err()
			case <-p.done:
				return z, ErrPoolClosed
			case c := <-p.idleConns:
				return c.conn, nil
			case <-p.slots:
				if conn, err := p.factoryFunc(); err != nil {
					p.slots <- struct{}{}
					return z, err
				} else {
					return conn, nil
				}
			}
		}
	}
}

// Puts connections to pool
func (p *Pool[T]) Put(conn T, close bool) error {
	p.wg.Add(1)
	defer p.wg.Done()

	t := time.Time{}
	if !close {
		t = time.Now()
	}

	return p.putConn(conn, t)
}

func (p *Pool[T]) putConn(conn T, lastActiveAt time.Time) error {
	select {
	case <-p.done:
		return ErrPoolClosed
	default:
	}

	if !lastActiveAt.IsZero() && lastActiveAt.After(time.Now().Add(-p.idleTimeout)) {
		select {
		case <-p.done:
			return ErrPoolClosed
		case p.idleConns <- idleConnWrap[T]{conn: conn, lastActiveAt: lastActiveAt}:
			return nil
		default:
		}
	}

	select {
	case <-p.done:
		return ErrPoolClosed
	case p.slots <- struct{}{}:
		return p.closeFunc(conn)
	default:
		return ErrPoolFull
	}
}

func (p *Pool[T]) startWorker() {
	go func() {
		p.wg.Add(1)
		defer p.wg.Done()

		for {
			select {
			case <-p.done:
				return
			case <-time.After(p.timeoutPrecision):
				checkIdle := func() {
					p.workerMutex.Lock()
					defer p.workerMutex.Unlock()

					idleConns := make(chan idleConnWrap[T], p.maxIdle)
				loop:
					for {
						select {
						case c := <-p.idleConns:
							idleConns <- c
						default:
							break loop
						}
					}
					close(idleConns)
					for c := range idleConns {
						_ = p.putConn(c.conn, c.lastActiveAt)
					}
				}
				checkIdle()
			}
		}
	}()
}

// current number of connections
func (p *Pool[T]) Len() int {
	return p.max - len(p.slots)
}

// Closes all idle connections
func (p *Pool[T]) ReleaseIdle() {
loop:
	for {
		select {
		case c := <-p.idleConns:
			_ = p.closeFunc(c.conn)
			p.slots <- struct{}{}
		default:
			break loop
		}
	}
}

// Releases pool. After close complete pool is not usable
func (p *Pool[T]) Close() {
	close(p.done)
	p.wg.Wait()
	p.ReleaseIdle()
	close(p.idleConns)
	close(p.slots)
	for _ = range p.slots {
	}
}
