package main

import (
	"github.com/google/uuid"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/stan.go"
	"google.golang.org/protobuf/proto"

	"app/hello"

	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"sync"
	"syscall"
	"time"
)

var pub = flag.Bool("p", false, "run in publisher mode")
var sub = flag.Bool("s", false, "run in subscriber mode")

type Runner interface {
	Start() error
	Stop(context.Context) error
	IsStopped() bool
}

type Subscriber struct {
	nc      *nats.Conn
	sc      stan.Conn
	topic   string
	port    int
	stopped bool
	mux     sync.RWMutex
	srv     *http.Server
}

type Publisher struct {
	nc      *nats.Conn
	sc      stan.Conn
	topic   string
	port    int
	stopped bool
	mux     sync.RWMutex
	srv     *http.Server
}

type Config struct {
	NATSClusterName string
	NATSUrl         string
	PublisherPort   int
	SubscriberPort  int
	Topic           string
}

func main() {
	flag.Parse()

	cfg := &Config{}
	if os.Getenv("ENVIRONMENT") == "" {
		if err := cfg.Default(); err != nil {
			die(err)
		}
	} else {
		if err := cfg.Populate(); err != nil {
			die(err)
		}
	}
	cfg.Print()

	var r Runner
	var err error
	if *pub {
		r, err = NewPublisher(cfg)
		if err != nil {
			die(err)
		}
	} else if *sub {
		r, err = NewSubscriber(cfg)
		if err != nil {
			die(err)
		}
	} else {
		die(fmt.Errorf("must set mode: -s (subscriber) or -p (publisher)"))
	}

	wrapSig(r)
	if err := r.Start(); err != nil {
		die(err)
	}
}

func NewPublisher(cfg *Config) (*Publisher, error) {

	nc, err := nats.Connect(cfg.NATSUrl)
	if err != nil {
		return nil, err
	}

	sc, err := stan.Connect(
		cfg.NATSClusterName,
		uuid.New().String(),
		stan.NatsConn(nc),
	)
	if err != nil {
		return nil, err
	}

	p := &Publisher{
		nc:    nc,
		sc:    sc,
		topic: cfg.Topic,
		port:  cfg.PublisherPort,
	}

	p.srv = &http.Server{
		Addr: fmt.Sprintf(":%d", p.port),
	}

	http.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "publisher healthy")
	})

	return p, nil
}

func (p *Publisher) Start() error {
	echan := make(chan error)
	dchan := make(chan bool)
	startHTTP(p.srv, echan)

	go func(dchan chan bool, echan chan error) {
		for i := 0; i >= 0; i++ {
			if p.IsStopped() {
				dchan <- true
				return
			}

			msg := hello.Hello{
				Content: fmt.Sprintf("hello world %d", i),
			}

			b, err := proto.Marshal(&msg)
			if err != nil {
				echan <- err
				return
			}
			p.sc.Publish(p.topic, b)

			time.Sleep(time.Second)
		}
	}(dchan, echan)

	for {
		select {
		case err := <-echan:
			return err
		case done := <-dchan:
			if done {
				return nil
			}
		}
	}
}

func (p *Publisher) Stop(ctx context.Context) error {
	log.Println("closing connections")
	if err := p.sc.Close(); err != nil {
		perr(err)
	}
	p.nc.Close()
	if err := p.srv.Shutdown(ctx); err != nil {
		return err
	}

	p.mux.Lock()
	defer p.mux.Unlock()
	p.stopped = true
	log.Println("publisher stopped")

	return nil
}

func (p *Publisher) IsStopped() bool {
	p.mux.RLock()
	defer p.mux.RUnlock()
	return p.stopped
}

func NewSubscriber(cfg *Config) (*Subscriber, error) {

	nc, err := nats.Connect(cfg.NATSUrl)
	if err != nil {
		return nil, err
	}

	sc, err := stan.Connect(
		cfg.NATSClusterName,
		uuid.New().String(),
		stan.NatsConn(nc),
	)
	if err != nil {
		return nil, err
	}

	s := &Subscriber{
		nc:    nc,
		sc:    sc,
		topic: cfg.Topic,
		port:  cfg.SubscriberPort,
	}

	s.srv = &http.Server{
		Addr: fmt.Sprintf(":%d", s.port),
	}

	http.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "subscriber healthy")
	})

	return s, nil
}

func (s *Subscriber) Start() error {
	echan := make(chan error)
	startHTTP(s.srv, echan)

	s.sc.Subscribe(s.topic, func(m *stan.Msg) {
		var msg hello.Hello
		err := proto.Unmarshal(m.Data, &msg)
		if err != nil {
			echan <- err
		}
		log.Printf("Received: %+v", msg.Content)
	}, stan.StartAtTime(time.Now().Add(-1*time.Minute)))

	for {
		if s.IsStopped() {
			return nil
		}
		select {
		case err := <-echan:
			return err
		default:
		}
	}
}

func (s *Subscriber) Stop(ctx context.Context) error {
	log.Println("closing connections")
	if err := s.sc.Close(); err != nil {
		perr(err)
	}
	s.nc.Close()
	if err := s.srv.Shutdown(ctx); err != nil {
		return err
	}

	s.mux.Lock()
	defer s.mux.Unlock()
	s.stopped = true
	log.Println("subscriber stopped")

	return nil
}

func (s *Subscriber) IsStopped() bool {
	s.mux.RLock()
	defer s.mux.RUnlock()
	return s.stopped
}

func (c *Config) Default() error {
	log.Println("default configuration enabled")
	c.NATSClusterName = "demo-stream"
	c.NATSUrl = nats.DefaultURL
	c.PublisherPort = 8080
	c.SubscriberPort = 8081
	c.Topic = "hello"
	return nil
}

func (c *Config) Populate() error {
	log.Println("populating configuration from environment")
	var err error

	c.NATSClusterName = os.Getenv("NATS_CLUSTER_NAME")
	c.NATSUrl = os.Getenv("NATS_URL")
	c.Topic = os.Getenv("TOPIC")

	pport := os.Getenv("PUBLISHER_PORT")
	if pport != "" {
		c.PublisherPort, err = strconv.Atoi(pport)
		if err != nil {
			return err
		}
	}

	sport := os.Getenv("SUBSCRIBER_PORT")
	if sport != "" {
		c.SubscriberPort, err = strconv.Atoi(sport)
		if err != nil {
			return err
		}
	}

	return nil
}

func (c *Config) Print() {
	log.Printf("Cluster Name: %s\n", c.NATSClusterName)
	log.Printf("Cluster URL: %s\n", c.NATSUrl)
	log.Printf("Topic: %s\n", c.Topic)
	if c.PublisherPort > 0 {
		log.Printf("Publisher Port: %d\n", c.PublisherPort)
	}
	if c.SubscriberPort > 0 {
		log.Printf("Subscriber Port: %d\n", c.SubscriberPort)
	}
}

func wrapSig(r Runner) {
	sigtermChan := make(chan os.Signal, 2)
	signal.Notify(sigtermChan, syscall.SIGTERM, os.Interrupt)
	go func() {
		sig := <-sigtermChan
		switch sig {
		case syscall.SIGINT:
			fallthrough
		case syscall.SIGTERM:
			log.Printf("%s receieved. Exiting...", sig.String())
			ctx, cancel := context.WithTimeout(
				context.Background(),
				5*time.Second,
			)
			defer cancel()
			if err := r.Stop(ctx); err != nil {
				fmt.Fprintf(
					os.Stderr,
					"error received: %+v",
					err,
				)
			}
		}
	}()
}

func startHTTP(srv *http.Server, echan chan error) {
	go func() {
		log.Printf("http: listening at %s\n", srv.Addr)
		if err := srv.ListenAndServe(); err != http.ErrServerClosed {
			echan <- err
		}
	}()
}

func die(err error) {
	log.Fatalf("Error: %+v\n", err)
}

func perr(err error) {
	log.Printf("Error: %+v\n", err)
}
