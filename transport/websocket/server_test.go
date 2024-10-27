package websocket

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"testing"
)

var testServer *Server

type User struct {
	Uid int
}

func connect(sessionID SessionID, a any) {
	u := a.(User)
	if u.Uid != 0 {
		//已登录
		fmt.Printf("sign in:%s", sessionID)
	} else {
		//未登录
		fmt.Printf("sign out:%s", sessionID)
	}
}

type Request struct {
	Id int64  `json:"id,omitempty"`
	Op string `json:"op,omitempty"`
}

type Reply struct {
	Code int
	Msg  string
	Data map[string]any
}

func Sub(ctx context.Context, sessionId SessionID, req *Request) (reply *Reply, err error) {
	reply = &Reply{
		Code: 0,
		Msg:  "success",
		Data: map[string]any{
			"foo": "bar",
		},
	}
	fmt.Printf("[%s] req: %v\nreply:%v", sessionId, req, reply)
	return reply, err
}

func TestServer(t *testing.T) {
	interrupt := make(chan os.Signal)
	signal.Notify(interrupt, syscall.SIGHUP, syscall.SIGINT, syscall.SIGTERM, syscall.SIGQUIT)

	ctx := context.Background()

	srv := NewServer(
		WithAddress(":8801"),
		WithPath("/"),
		WithConnectHandle(connect),
	)

	RegisterServerMessageHandler(srv, "sub", Sub)
	testServer = srv

	if err := srv.Start(ctx); err != nil {
		panic(err)
	}

	defer func() {
		if err := srv.Stop(ctx); err != nil {
			t.Errorf("expected nil got %v", err)
		}
	}()

	<-interrupt
}
