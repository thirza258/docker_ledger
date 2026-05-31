package docker

import (
	"context"
	"github.com/moby/moby/client"
	"log"
	"sync"
)

var (
	once   sync.Once
	clientInstance *client.Client
)

func GetClient() (*client.Client, error) {
	var err error
	once.Do(func() {
		cli, e := client.NewClientWithOpts(client.FromEnv, client.WithUserAgent("dockerledger/1.0.0"))
		if e != nil {
			err = e
			return
		}
		clientInstance = cli
		log.Println("Docker client initialized")
	})
	return clientInstance, err
}

func Ping(ctx context.Context) error {
	cli, err := GetClient()
	if err != nil {
		return err
	}
	_, err = cli.Ping(ctx, client.PingOptions{})
	return err
}



func Close() error {
	if clientInstance != nil {
		return clientInstance.Close()
	}
	return nil
}