package main

import (
	"context"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"raspberrysour/api"
	"raspberrysour/pb"

	"github.com/jmoiron/sqlx"
	_ "github.com/lib/pq"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

const (
	host     = "db"
	port     = 5432
	user     = "admin"
	password = "1234"
	dbname   = "admin"
)

func loadPrivateKey() (*rsa.PrivateKey, error) {
	keyData, err := os.ReadFile("keys/private.pem")
	if err != nil {
		return nil, fmt.Errorf("could not open private key file")
	}

	block, _ := pem.Decode(keyData)
	if block == nil {
		return nil, fmt.Errorf("failed to decode private key file")
	}

	privateKey, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("failed to parse private key")
	}

	rsaPrivateKey, ok := privateKey.(*rsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("parsed private key was not an RSA private key")
	}

	return rsaPrivateKey, nil
}

func loadPublicKey() (*rsa.PublicKey, error) {
	keyData, err := os.ReadFile("keys/public.pem")
	if err != nil {
		return nil, fmt.Errorf("could not open public key file")
	}

	block, _ := pem.Decode(keyData)
	if block == nil {
		return nil, fmt.Errorf("failed to decode public key file")
	}

	publicKey, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("failed to parse public key")
	}

	rsaPublicKey, ok := publicKey.(*rsa.PublicKey)
	if !ok {
		return nil, fmt.Errorf("parsed public key was not an RSA public key")
	}

	return rsaPublicKey, nil
}

func initDB() *sqlx.DB {
	psqlInfo := fmt.Sprintf("host=%s port=%d user=%s password=%s dbname=%s sslmode=disable", host, port, user, password, dbname)
	db, err := sqlx.Open("postgres", psqlInfo)
	if err != nil {
		panic(err)
	}

	err = db.Ping()
	if err != nil {
		panic(err)
	}
	fmt.Println("Connected!")

	return db
}

func grpcMiddleware(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (interface{}, error) {
	_, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return nil, status.Error(codes.Unauthenticated, "missing metadata")
	}

	return handler(ctx, req)
}

func main() {
	db := initDB()
	defer db.Close()

	privateKey, err := loadPrivateKey()
	if err != nil {
		fmt.Println(err.Error())
		os.Exit(1)
	}

	publicKey, err := loadPublicKey()
	if err != nil {
		fmt.Println(err.Error())
		os.Exit(1)
	}

	env := api.NewRequestEnvironment(db, privateKey, publicKey)

	middlewares := []api.Middleware{
		env.VersionMiddleWare,
		env.UserMiddleWare,
	}

	middleware := func(f func(w http.ResponseWriter, r *http.Request) (string, error)) func(w http.ResponseWriter, r *http.Request) (string, error) {
		return func(w http.ResponseWriter, r *http.Request) (string, error) {
			var req = r
			for _, m := range middlewares {
				tempreq, err := m(req)
				if err != nil {
					return "", err
				}
				req = tempreq
			}
			str, err := f(w, req)
			return str, err
		}
	}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /temp-log", api.ResponseHandler(middleware(env.PostTempLog)))
	mux.HandleFunc("GET /temp-log", api.ResponseHandler(middleware(env.GetTempLogs)))
	mux.HandleFunc("GET /temp-log/{id}", api.ResponseHandler(middleware(env.GetTempLog)))
	mux.HandleFunc("POST /login", api.ResponseHandler(middleware(env.Login)))
	mux.HandleFunc("POST /register", api.ResponseHandler(middleware(env.Register)))
	mux.HandleFunc("GET /user", api.ResponseHandler(middleware(env.GetUsers)))

	ctx, cancelCtx := context.WithCancel(context.Background())
	server := &http.Server{
		Addr:    ":3333",
		Handler: mux,
		BaseContext: func(l net.Listener) context.Context {
			return ctx
		},
	}

	go func() {
		err := server.ListenAndServe()

		if errors.Is(err, http.ErrServerClosed) {
			fmt.Printf("Server closed\n")
		} else if err != nil {
			fmt.Printf("Error starting server %s\n", err)
			os.Exit(1)
		}

		cancelCtx()
	}()

	lis, err := net.Listen("tcp", fmt.Sprintf(":%d", 50051))
	if err != nil {
		log.Fatalf("Failed to listen: %v\n", err)
	}

	s := grpc.NewServer(
		grpc.UnaryInterceptor(grpcMiddleware),
	)
	pb.RegisterAPIServer(s, &pb.Server{
		DB: db,
	})
	log.Printf("server listening at %v", lis.Addr())
	if err := s.Serve(lis); err != nil {
		log.Fatalf("failed to serve: %v", err)
	}

	<-ctx.Done()
}
