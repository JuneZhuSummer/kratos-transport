package websocket

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/JuneZhuSummer/kratos-transport/transport/websocket/redis/ps_dispatcher"
	"github.com/go-redis/redis/v8"

	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/go-kratos/kratos/v2/log"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"
)

const (
	DispatcherPoolPublic  = "public"
	DispatcherPoolPrivate = "private"
)

var channelBufSize = 1024

type SessionID string

type ConnectHandler func(SessionID, any)

var PrivateHandler func(msg *redis.Message) ([]byte, error)

func SetPrivateHandler(f func(msg *redis.Message) ([]byte, error)) {
	PrivateHandler = f
}

type ClientInfo struct {
	ID     SessionID
	IP     string
	UA     string
	Origin string
	IsAuth bool
	Token  string
}

type Session struct {
	ctx    context.Context
	cancel context.CancelFunc

	client *ClientInfo

	wsConn *websocket.Conn
	server *Server

	pushChan chan []byte

	pubDispatcher *ps_dispatcher.MultiChannelDispatcher
	priDispatcher *ps_dispatcher.MultiChannelDispatcher

	lastSendTime int64

	log *log.Helper
}

func NewSession(wsConn *websocket.Conn, r *http.Request, server *Server) *Session {

	ctx, cancel := context.WithCancel(context.Background())

	u, _ := uuid.NewUUID()

	session := &Session{
		ctx:    ctx,
		cancel: cancel,
		client: &ClientInfo{
			ID:     SessionID(u.String()),
			IP:     strings.Split(r.Header.Get("X-Forwarded-For"), ",")[0],
			UA:     r.Header.Get("User-Agent"),
			Origin: r.Header.Get("Origin"),
			IsAuth: false,
			Token:  "",
		},
		wsConn:        wsConn,
		server:        server,
		pushChan:      make(chan []byte, channelBufSize),
		pubDispatcher: &ps_dispatcher.MultiChannelDispatcher{},
		priDispatcher: &ps_dispatcher.MultiChannelDispatcher{},
		lastSendTime:  0,
		log:           log.NewHelper(log.GetLogger(), log.WithMessageKey("Session")),
	}

	pubMdp, err := ps_dispatcher.GetMultiChannelDispatcherPool(DispatcherPoolPublic)
	if err != nil {
		panic(err)
	}
	session.pubDispatcher.Init(pubMdp)

	priMdp, err := ps_dispatcher.GetMultiChannelDispatcherPool(DispatcherPoolPrivate)
	if err != nil {
		panic(err)
	}
	session.priDispatcher.Init(priMdp)

	return session
}

func (s *Session) Conn() *websocket.Conn {
	return s.wsConn
}

func (s *Session) SessionID() SessionID {
	return s.client.ID
}

func (s *Session) PubDispatcher() *ps_dispatcher.MultiChannelDispatcher {
	return s.pubDispatcher
}

func (s *Session) PriDispatcher() *ps_dispatcher.MultiChannelDispatcher {
	return s.priDispatcher
}

func (s *Session) IsAuth() bool {
	return s.client.IsAuth
}

func (s *Session) Client() *ClientInfo {
	return s.client
}

func (s *Session) SetToken(token string) {
	s.client.IsAuth = true
	s.client.Token = token
}

// 读--转到messageHandler里处理
func (s *Session) dealRequest() {
	var (
		messageType int
		msgContent  []byte
		err         error
	)

	for {
		select {
		case <-s.ctx.Done():
			_ = s.wsConn.Close()
			return
		default:
			// 60s如果没有发送新的请求（ping）过来则断开
			if err = s.wsConn.SetReadDeadline(time.Now().Add(60 * time.Second)); err != nil {
				s.log.Infof("Service:Session:dealSubscribeRequest set read dead line fail, err: %s, clientInfo: %+v", err, s.client)
				return
			}
			if messageType, msgContent, err = s.wsConn.ReadMessage(); err == nil {
				err = s.server.messageHandle(s.ctx, s.client.ID, messageType, msgContent)
				if err != nil {
					errMsg := map[string]any{
						"code": -1,
						"msg":  fmt.Sprintf("format err:%v", err),
					}
					marshal, _ := json.Marshal(errMsg)
					s.pushToChan(marshal)
				}
			} else {
				if !strings.Contains(err.Error(), "websocket: close 1001") && !strings.Contains(err.Error(), "websocket: close 1006") {
					s.log.Errorf("Service:Session:dealRequest, s.wsConn.ReadMessage fail, err: %s, clientInfo: %+v", err, s.client)
				} else {
					s.log.Debugf("Service:Session:dealRequest, s.wsConn.ReadMessage fail, err: %s, clientInfo: %+v", err, s.client)
				}
				s.cancel()
			}
		}
	}
}

// 写--向client发送消息
func (s *Session) sendToClient() {
	for {
		select {
		case <-s.ctx.Done():
			close(s.pushChan)
			return
		case msg, ok := <-s.pushChan:
			if !ok {
				return
			}
			_ = s.wsConn.SetWriteDeadline(time.Now().Add(time.Second * 5))
			if err := s.wsConn.WriteMessage(s.server.writeMessageType, msg); err != nil {
				s.log.Errorf("Service:Session:sendToClient s.wsConn.WriteMessage fail, err: %s, clientInfo: %+v", err, s.client)
				s.cancel()
				return
			}
			s.lastSendTime = time.Now().UnixNano()
		}
	}
}

// 主动推送
func (s *Session) receiveMsg() {
	go func() {
		for {
			select {
			case <-s.ctx.Done():
				s.pubDispatcher.Close()
				return
			case msg := <-s.pubDispatcher.Channel():
				switch t := msg.(type) {
				case *redis.Message:
					s.pushToChan([]byte(t.Payload))
				default:
					continue
				}
			}
		}
	}()

	for {
		select {
		case <-s.ctx.Done():
			s.priDispatcher.Close()
			return
		case msg := <-s.priDispatcher.Channel():
			switch t := msg.(type) {
			case *redis.Message:
				if b, err := PrivateHandler(t); err == nil {
					s.pushToChan(b)
				}
			default:
				continue
			}
		}
	}
}

func (s *Session) pushToChan(msg []byte) {
	if len(s.pushChan) > (channelBufSize - 50) {
		//通道即将满，kill session
		s.log.Debugf("Service:Session:pushToChan len(s.pushChan) > (channelBufSize - 50), close connection. msg: %s, clientInfo: %+v", string(msg), s.client)
		s.cancel()
	} else {
		if s.ctx.Err() == nil {
			s.pushChan <- msg
		}
	}
}

func (s *Session) Listen() {
	go s.dealRequest()
	go s.sendToClient()
	go s.receiveMsg()
}

type SessionManager struct {
	sessions       map[SessionID]*Session
	mu             *sync.RWMutex
	connectHandler ConnectHandler
}

func NewSessionManager() *SessionManager {
	return &SessionManager{
		sessions: make(map[SessionID]*Session),
		mu:       new(sync.RWMutex),
	}
}

func (s *SessionManager) RegisterConnectHandler(handler ConnectHandler) {
	s.connectHandler = handler
}

func (s *SessionManager) Clear() {
	s.mu.Lock()
	defer s.mu.Unlock()
	clear(s.sessions)
}

func (s *SessionManager) Count() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.sessions)
}

func (s *SessionManager) Get(sessionId SessionID) (session *Session, ok bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	session, ok = s.sessions[sessionId]
	return session, ok
}

func (s *SessionManager) Range(fn func(*Session)) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, v := range s.sessions {
		fn(v)
	}
}

func (s *SessionManager) Add(c *Session) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.sessions[c.SessionID()] = c
}

func (s *SessionManager) Remove(c *Session) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for k, v := range s.sessions {
		if c == v {
			delete(s.sessions, k)
			return
		}
	}
}
