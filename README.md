# GoBlockchain

A simple blockchain as an exercise.

### Running locally
```shell
$ go run main.go
```

### API Calls

#### GET
To list all existent blocks:

```shell
$ curl http://localhost:8080
```

#### POST
To create a new block:
```shell
$ curl -H "Content-Type: application/json" -d '{"BPM":10}' http://localhost:8080
```
