// API de Sphere en Go: port 1:1 del backend Django (sphere/).
// Usa la misma base PostgreSQL, las mismas rutas, cookies y JWT, así que
// puede reemplazar al proceso de Python sin cambios en el front.
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

	"sphere-api/internal/app"
)

func main() {
	// Igual que load_dotenv(): carga .env si existe, sin pisar variables ya definidas
	_ = godotenv.Load()

	cfg := app.LoadConfig()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	a, err := app.New(ctx, cfg)
	if err != nil {
		log.Fatalf("no se pudo iniciar la API: %v", err)
	}
	defer a.Close()

	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           a.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	go func() {
		log.Printf("API Sphere escuchando en %s", cfg.Addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("error del servidor: %v", err)
		}
	}()

	<-ctx.Done()
	log.Print("cerrando servidor...")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdownCtx)
}
