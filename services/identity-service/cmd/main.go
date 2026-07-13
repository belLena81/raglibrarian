package main

import (
	"context"
	"encoding/hex"
	"net"
	"os"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/grpc"
	"go.uber.org/zap"

	"github.com/belLena81/raglibrarian/pkg/auth"
	"github.com/belLena81/raglibrarian/pkg/logger"
	identityv1 "github.com/belLena81/raglibrarian/pkg/proto/identity/v1"
	identitygrpc "github.com/belLena81/raglibrarian/services/identity-service/grpc"
	"github.com/belLena81/raglibrarian/services/identity-service/repository"
	"github.com/belLena81/raglibrarian/services/identity-service/usecase"
)

func main() {
	log := logger.Must("identity-service"); defer log.Sync()
	dsn, keyHex := os.Getenv("IDENTITY_POSTGRES_DSN"), os.Getenv("AUTH_SECRET_KEY")
	if dsn == "" || keyHex == "" { log.Fatal("IDENTITY_POSTGRES_DSN and AUTH_SECRET_KEY are required") }
	key, err := hex.DecodeString(keyHex); if err != nil { log.Fatal("invalid AUTH_SECRET_KEY", zap.Error(err)) }
	pool, err := pgxpool.New(context.Background(), dsn); if err != nil { log.Fatal("database connection failed", zap.Error(err)) }; defer pool.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second); defer cancel(); if err = pool.Ping(ctx); err != nil { log.Fatal("database unavailable", zap.Error(err)) }
	issuer, err := auth.NewIssuer(key, 24*time.Hour); if err != nil { log.Fatal("invalid auth configuration", zap.Error(err)) }
	listener, err := net.Listen("tcp", env("IDENTITY_GRPC_ADDR", ":50051")); if err != nil { log.Fatal("listen failed", zap.Error(err)) }
	server := grpc.NewServer(); identityv1.RegisterIdentityServiceServer(server, identitygrpc.NewServer(usecase.NewAuthService(repository.NewPostgresUserRepository(pool), issuer)))
	log.Info("identity service starting"); if err = server.Serve(listener); err != nil { log.Fatal("server exited", zap.Error(err)) }
}
func env(key, fallback string) string { if value := os.Getenv(key); value != "" { return value }; return fallback }
