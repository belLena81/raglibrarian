// cmd/query is the entry point for the Query Service.
// Wires all dependencies, seeds admin, starts HTTP + gRPC servers.
package main

import (
	"context"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/grpc"
	"google.golang.org/grpc/reflection"

	authpb "github.com/yourname/raglibrarian/pkg/proto/auth"
	appauth "github.com/yourname/raglibrarian/services/query/internal/application/auth"
	bcryptpkg "github.com/yourname/raglibrarian/services/query/internal/infrastructure/bcrypt"
	"github.com/yourname/raglibrarian/services/query/internal/infrastructure/paseto"
	pginfra "github.com/yourname/raglibrarian/services/query/internal/infrastructure/postgres"
	authhandler "github.com/yourname/raglibrarian/services/query/internal/transport/http/handler"
	"github.com/yourname/raglibrarian/services/query/internal/transport/http/router"
	grpcinterceptor "github.com/yourname/raglibrarian/services/query/internal/transport/grpc/interceptor"
)

func main() {
	ctx := context.Background()

	dbURL     := mustEnv("DATABASE_URL")
	pasetoKey := mustEnv32Bytes("PASETO_SYMMETRIC_KEY")
	httpAddr  := envOr("HTTP_ADDR", ":8080")
	grpcAddr  := envOr("GRPC_ADDR", ":9090")
	adminEmail := os.Getenv("ADMIN_EMAIL")
	adminPass  := os.Getenv("ADMIN_PASSWORD")

	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		log.Fatalf("postgres connect: %v", err)
	}
	defer pool.Close()

	userRepo  := pginfra.NewUserRepository(pool)
	tokenRepo := pginfra.NewRefreshTokenRepository(pool)
	hasher    := bcryptpkg.New()
	issuer, err := paseto.NewIssuer(pasetoKey, 15*time.Minute)
	if err != nil {
		log.Fatalf("paseto: %v", err)
	}

	svc := appauth.NewService(userRepo, tokenRepo, hasher, issuer, appauth.DefaultConfig())

	if adminEmail != "" && adminPass != "" {
		if err = svc.SeedAdmin(ctx, adminEmail, adminPass); err != nil {
			log.Fatalf("seed admin: %v", err)
		}
		log.Printf("admin account ready: %s", adminEmail)
	}

	// gRPC server — exposes AuthService for Metadata + Retrieval
	grpcSrv := grpc.NewServer()
	authpb.RegisterAuthServiceServer(grpcSrv, grpcinterceptor.NewAuthServiceServer(issuer))
	reflection.Register(grpcSrv)

	grpcLis, err := net.Listen("tcp", grpcAddr)
	if err != nil {
		log.Fatalf("grpc listen: %v", err)
	}
	go func() {
		log.Printf("gRPC listening on %s", grpcAddr)
		if err := grpcSrv.Serve(grpcLis); err != nil {
			log.Fatalf("grpc serve: %v", err)
		}
	}()

	// HTTP server
	httpSrv := &http.Server{
		Addr:         httpAddr,
		Handler:      router.New(authhandler.New(svc), issuer),
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 10 * time.Second,
		IdleTimeout:  60 * time.Second,
	}
	go func() {
		log.Printf("HTTP listening on %s", httpAddr)
		if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("http serve: %v", err)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	log.Println("shutting down...")
	shutCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	_ = httpSrv.Shutdown(shutCtx)
	grpcSrv.GracefulStop()
}

func mustEnv(key string) string {
	v := os.Getenv(key)
	if v == "" {
		log.Fatalf("required env var %s is not set", key)
	}
	return v
}

func mustEnv32Bytes(key string) []byte {
	v := mustEnv(key)
	if len(v) != 32 {
		log.Fatalf("env var %s must be exactly 32 bytes, got %d", key, len(v))
	}
	return []byte(v)
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
