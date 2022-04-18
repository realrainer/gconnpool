# gconnpool
Generic connection pool for golang

### USAGE

```go
// create a connection pool:
// initial connections: 2
// maximum connections: 10
// maximum idle connections: 2
// idle timeout: 2 min (after this time idle connection closed)
pool, err := gconnpool.New[net.Conn](&gconnpool.Config{
	    Initial: 2,
	    Max: 10,
	    MaxIdle: 2,
	    IdleTimeout: time.Minute * 2,
    },
    // factory function
    func() (net.Conn, error) {
        return net.Dial("tcp", "127.0.0.1:1234")
    },
    // close function
    func(conn net.Conn) error {
        return conn.Close()
    },
)

// get connection from pool
conn, err := pool.Get()

// ...
// do anything with conn (but don't close)

// put connection to pool and not close
err := pool.Put(conn, false)
// or put connection to pool and close
err := pool.Put(conn, true)

// _IMPORTANT!_ putting into poll is necessary!

// get number of opened connections
l := pool.Len()

// close pool if not needed:
pool.Close()


```
