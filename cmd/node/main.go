package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/joho/godotenv"
	"github.com/petersonsalme/blockchain-with-go/internal/chain"
	"github.com/petersonsalme/blockchain-with-go/internal/consensus/pow"
	"github.com/petersonsalme/blockchain-with-go/internal/server"
	"github.com/petersonsalme/blockchain-with-go/internal/storage/memory"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	fatalIf(bootstrap(ctx), log.Fatal)
}

// fatalIf calls fatal with err unless err is nil.
func fatalIf(err error, fatal func(v ...any)) {
	if err != nil {
		fatal(err)
	}
}

// bootstrap loads .env and runs the node until ctx is done.
func bootstrap(ctx context.Context) error {
	if err := godotenv.Load(); err != nil {
		return err
	}
	return run(ctx)
}

// shutdownContext bounds the graceful shutdown to 10 seconds.
func shutdownContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 10*time.Second)
}

// run wires repository, consensus, service and server, then serves until ctx is done.
func run(ctx context.Context) error {
	repo := memory.New()
	engine := pow.NewProofOfWorkFromEnv()

	svc, err := chain.NewService(repo, engine)
	if err != nil {
		return err
	}

	srv := server.NewServer(svc, server.Config{
		MiningTimeout: server.ParseMiningTimeout(os.Getenv(server.MiningTimeoutEnvVar)),
	})

	addr := ":" + os.Getenv("PORT")
	httpSrv := srv.HTTPServer(addr)

	errCh := make(chan error, 1)
	go func() {
		log.Println("Listening on ", addr)
		errCh <- httpSrv.ListenAndServe()
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
	}

	shutdownCtx, cancel := shutdownContext()
	defer cancel()
	if err := httpSrv.Shutdown(shutdownCtx); err != nil {
		return err
	}
	if err := <-errCh; !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
