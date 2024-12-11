package ps_dispatcher

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/go-redis/redis/v8"
)

var (
	multiDispatcherMap = sync.Map{} //make(map[string]*SingleChannelDispatcherPool)
)

func GetMultiChannelDispatcherPool(alias string) (*MultiChannelDispatcherPool, error) {
	if dispatchLoad, ok := multiDispatcherMap.Load(alias); ok && dispatchLoad.(*MultiChannelDispatcherPool).ctx != nil {
		return dispatchLoad.(*MultiChannelDispatcherPool), nil
	}
	return nil, fmt.Errorf("pubsubDispatcher:GetMultiChannelDispatcherPool dispatcher not fund")
}

func RegisterMultiChannelDispatcherPool(alias string, redisClient *redis.Client, mode dispatcherMode) (mdp *MultiChannelDispatcherPool, err error) {
	if _, ok := multiDispatcherMap.Load(alias); ok {
		return nil, fmt.Errorf("pubsubDispatcher:RegisterMultiChannelDispatcherPool fail. alias: %s has allready used. ", alias)
	}
	multiDispatcherMap.Store(alias, &MultiChannelDispatcherPool{})
	defer func() {
		if err != nil {
			multiDispatcherMap.Delete(alias)
		}
		if dispatcherLoad, ok := multiDispatcherMap.Load(alias); !ok || dispatcherLoad.(*MultiChannelDispatcherPool).ctx == nil {
			multiDispatcherMap.Delete(alias)
		}
	}()
	mdp = NewMultiChannelDispatcherPool(context.Background(), redisClient, mode)
	multiDispatcherMap.Store(alias, mdp)
	return mdp, nil
}

type MultiChannelDispatcher struct {
	mu             sync.RWMutex
	pubChannel     chan interface{}
	subChannels    sync.Map // map[string]struct{}
	subPatterns    sync.Map // map[string]struct{}
	subChannelsLen int
	subPatternsLen int
	mdp            *MultiChannelDispatcherPool
}

func (md *MultiChannelDispatcher) Init(mdp *MultiChannelDispatcherPool) {
	md.pubChannel = make(chan interface{}, 100)
	md.subChannels = sync.Map{}
	md.subPatterns = sync.Map{}
	md.mdp = mdp
	mdp.AddDispatcher(md)
}

func (md *MultiChannelDispatcher) Channel() chan interface{} {
	return md.pubChannel
}

func (md *MultiChannelDispatcher) String() string {
	buf := bytes.Buffer{}
	md.subChannels.Range(func(channel, _ interface{}) bool {
		buf.WriteString(channel.(string))
		buf.WriteString(",")
		return true
	})
	md.subPatterns.Range(func(pattern, _ interface{}) bool {
		buf.WriteString(pattern.(string))
		buf.WriteString(",")
		return true
	})
	channels := buf.String()
	return channels[:len(channels)-1]
}

func (md *MultiChannelDispatcher) Channels() []string {
	channels := make([]string, 0)
	md.subChannels.Range(func(channel, _ interface{}) bool {
		channels = append(channels, channel.(string))
		return true
	})
	md.subPatterns.Range(func(pattern, _ interface{}) bool {
		channels = append(channels, pattern.(string))
		return true
	})
	return channels
}

func (md *MultiChannelDispatcher) ChannelsSize() int {
	return md.subChannelsLen
}

func (md *MultiChannelDispatcher) PatternsSize() int {
	return md.subPatternsLen
}

func (md *MultiChannelDispatcher) Subscribe(channels ...string) {
	for k := 0; k < len(channels); k++ {
		if _, ok := md.subChannels.Load(channels[k]); ok {
			channels = append(channels[0:k], channels[k+1:]...)
			k--
			continue
		}
		md.subChannelsLen++
		md.subChannels.Store(channels[k], struct{}{})
	}

	for _, s := range channels {
		md.mdp.subChannelChan <- s
	}
}

func (md *MultiChannelDispatcher) Unsubscribe(channels ...string) {
	for k := 0; k < len(channels); k++ {
		if _, ok := md.subChannels.Load(channels[k]); !ok {
			channels = append(channels[0:k], channels[k+1:]...)
			k--
			continue
		}
		md.subChannelsLen--
		md.subChannels.Delete(channels[k])
	}

	for _, s := range channels {
		md.mdp.unsubChannelChan <- s
	}
}

func (md *MultiChannelDispatcher) PSubscribe(patterns ...string) {
	for k := 0; k < len(patterns); k++ {
		if _, ok := md.subPatterns.Load(patterns[k]); ok {
			patterns = append(patterns[0:k], patterns[k+1:]...)
			k--
			continue
		}
		md.subPatternsLen++
		md.subPatterns.Store(patterns[k], struct{}{})
	}

	for _, p := range patterns {
		md.mdp.subChannelChan <- p
	}
}

func (md *MultiChannelDispatcher) PUnsubscribe(patterns ...string) {
	for k := 0; k < len(patterns); k++ {
		if _, ok := md.subPatterns.Load(patterns[k]); !ok {
			patterns = append(patterns[0:k], patterns[k+1:]...)
			continue
		}
		md.subPatternsLen--
		md.subPatterns.Delete(patterns[k])
	}

	for _, p := range patterns {
		md.mdp.unsubChannelChan <- p
	}
}

func (md *MultiChannelDispatcher) Close() {
	md.subChannels.Range(func(channel, _ interface{}) bool {
		md.mdp.unsubChannelChan <- channel.(string)
		md.subChannels.Delete(channel)
		return true
	})

	md.subPatterns.Range(func(pattern, _ interface{}) bool {
		md.mdp.unsubChannelChan <- pattern.(string)
		md.subPatterns.Delete(pattern)
		return true
	})
	md.mdp.DelDispatcher(md)
	close(md.pubChannel)
}

func (md *MultiChannelDispatcher) pub(msg *redis.Message, receiveData interface{}) {
	if msg.Pattern == "" {
		if _, ok := md.subChannels.Load(msg.Channel); ok {
			if len(md.pubChannel) < 90 {
				md.pubChannel <- receiveData
			}
		}
	} else if md.subPatternsLen > 0 {
		if _, ok := md.subPatterns.Load(msg.Pattern); ok {
			if len(md.pubChannel) < 90 {
				md.pubChannel <- receiveData
			}
		}
	}
}

type MultiChannelDispatcherPool struct {
	ctx               context.Context
	mode              dispatcherMode
	redisClient       *redis.Client
	subChannels       map[string]int64
	redisSub          *redis.PubSub
	redisSubMap       map[string]*redis.PubSub
	addDispatcherChan chan *MultiChannelDispatcher
	delDispatcherChan chan *MultiChannelDispatcher
	dispatcherList    []*MultiChannelDispatcher
	subChannelChan    chan string
	unsubChannelChan  chan string
	f                 ProcessFunc

	subChannelsLen int64
	subCount       int64
	unsubCount     int64
	connectCount   int64
	breakCount     int64
}

type dispatcherMode uint8

const (
	DispatcherModeSingleConn dispatcherMode = 1
	DispatcherModeMultiConn  dispatcherMode = 2
)

func NewMultiChannelDispatcherPool(ctx context.Context, redisClient *redis.Client, mode dispatcherMode) (mdp *MultiChannelDispatcherPool) {
	if ctx == nil {
		ctx = context.Background()
	}
	mdp = &MultiChannelDispatcherPool{
		ctx:               ctx,
		redisClient:       redisClient,
		subChannels:       make(map[string]int64),
		addDispatcherChan: make(chan *MultiChannelDispatcher, 100),
		delDispatcherChan: make(chan *MultiChannelDispatcher, 100),
		dispatcherList:    make([]*MultiChannelDispatcher, 0),
		subChannelChan:    make(chan string, 2000),
		unsubChannelChan:  make(chan string, 2000),
	}

	if mode == DispatcherModeSingleConn {
		mdp.redisSub = redisClient.Subscribe(ctx)
		go mdp.dealSubscribeRequestAndReceive()
	} else if mode == DispatcherModeMultiConn {
		mdp.redisSubMap = make(map[string]*redis.PubSub)
		go mdp.dealSubscribeRequest()
	}

	go mdp.dealDispatcherRequest()

	return mdp
}

func (mdp *MultiChannelDispatcherPool) AddProcessFunc(f ProcessFunc) {
	if f != nil {
		mdp.f = f
	}
}

func (mdp *MultiChannelDispatcherPool) PrintLength(duration time.Duration) {
	timer := time.NewTicker(duration)
	go func() {
		for {
			select {
			case <-mdp.ctx.Done():
				return
			case <-timer.C:
				log.Printf("pubsubDispatcher:MultiChannelDispatcherPool:PrintLength subChannelsLen: %d, redisChanListLen: %d", mdp.subChannelsLen, len(mdp.dispatcherList))
				log.Printf("pubsubDispatcher:MultiChannelDispatcherPool:PrintLength SubCount: %d, UnsubCount: %d, ConnectCount: %+d, BreakCount: %+d", mdp.subCount, mdp.unsubCount, mdp.connectCount, mdp.breakCount)
			}
		}
	}()
}

func (mdp *MultiChannelDispatcherPool) dealSubscribeRequestAndReceive() {
	var (
		receiveData interface{}
		err         error
	)
	for {
		select {
		case <-mdp.ctx.Done():
			close(mdp.delDispatcherChan)
			close(mdp.addDispatcherChan)
			close(mdp.subChannelChan)
			close(mdp.unsubChannelChan)
			return
		case channel, ok := <-mdp.subChannelChan:
			if ok {
				mdp.subCount++
				if _, ok := mdp.subChannels[channel]; !ok {
					mdp.subChannels[channel] = 1
					if strings.Contains(channel, "*") || strings.Contains(channel, "?") {
						_ = mdp.redisSub.PSubscribe(mdp.ctx, channel)
					} else {
						_ = mdp.redisSub.Subscribe(mdp.ctx, channel)
					}
					mdp.subChannelsLen++
				} else {
					mdp.subChannels[channel]++
				}
			}
		case channel, ok := <-mdp.unsubChannelChan:
			if ok {
				mdp.unsubCount++
				if _, ok := mdp.subChannels[channel]; ok {
					mdp.subChannels[channel]--
					if mdp.subChannels[channel] <= 0 {
						delete(mdp.subChannels, channel)
						mdp.subChannelsLen--
						if strings.Contains(channel, "*") || strings.Contains(channel, "?") {
							_ = mdp.redisSub.PUnsubscribe(mdp.ctx, channel)
						} else {
							_ = mdp.redisSub.Unsubscribe(mdp.ctx, channel)
						}
					}
				}
			}
		case msg, ok := <-mdp.redisSub.Channel():
			if !ok {
				continue
			}
			receiveData = msg
			if mdp.f != nil {
				receiveData, err = mdp.f(msg)
				if err != nil {
					//log.Printf("MultiChannelDispatcherPool:dealSubscribeRequestAndReceive run processFunc fail, err: %s", err)
					continue
				}
			}
			for _, dispatcher := range mdp.dispatcherList {
				dispatcher.pub(msg, receiveData)
			}
		}
	}
}

func (mdp *MultiChannelDispatcherPool) dealSubscribeRequest() {
	for {
		select {
		case <-mdp.ctx.Done():
			return
		case channel, ok := <-mdp.subChannelChan:
			if ok {
				mdp.subCount++
				if _, ok := mdp.subChannels[channel]; !ok {
					mdp.subChannels[channel] = 1
					sub := mdp.redisClient.Subscribe(mdp.ctx)
					mdp.redisSubMap[channel] = sub
					mdp.subChannelsLen++
					go mdp.receiveAndPushByChannel(sub, channel)
				} else {
					mdp.subChannels[channel]++
				}
			}
		case channel, ok := <-mdp.unsubChannelChan:
			if ok {
				mdp.unsubCount++
				if _, ok := mdp.subChannels[channel]; ok {
					mdp.subChannels[channel]--
					if mdp.subChannels[channel] <= 0 {
						delete(mdp.subChannels, channel)
						mdp.subChannelsLen--
						if sub, ok := mdp.redisSubMap[channel]; ok {
							if err := sub.Close(); err != nil {
								log.Printf("pubsubDispatcher:MultiChannelDispatcherPool:dealSubscribeRequest close redis sub err: %s", err)
							}
						}
					}
				}
			}
		}
	}
}

func (mdp *MultiChannelDispatcherPool) receiveAndPushByChannel(sub *redis.PubSub, channel string) {
	var (
		receiveData interface{}
		err         error
	)
	if strings.Contains(channel, "*") || strings.Contains(channel, "?") {
		_ = sub.PSubscribe(mdp.ctx, channel)
	} else {
		_ = sub.Subscribe(mdp.ctx, channel)
	}
	ch := sub.Channel()
	for {
		select {
		case <-mdp.ctx.Done():
			_ = sub.Close()
			return
		case msg, ok := <-ch:
			if !ok {
				return
			}
			receiveData = msg
			if mdp.f != nil {
				receiveData, err = mdp.f(msg)
				if err != nil {
					//log.Printf("MultiChannelDispatcherPool:receiveAndPushByChannel run processFunc fail, err: %s", err)
					continue
				}
			}
			for _, dispatcher := range mdp.dispatcherList {
				dispatcher.pub(msg, receiveData)
			}
		}
	}
}

func (mdp *MultiChannelDispatcherPool) AddDispatcher(dispatcher *MultiChannelDispatcher) {
	mdp.addDispatcherChan <- dispatcher
}

func (mdp *MultiChannelDispatcherPool) DelDispatcher(dispatcher *MultiChannelDispatcher) {
	mdp.delDispatcherChan <- dispatcher
}

func (mdp *MultiChannelDispatcherPool) dealDispatcherRequest() {
	for {
		select {
		case <-mdp.ctx.Done():
			close(mdp.delDispatcherChan)
			close(mdp.addDispatcherChan)
			return
		case dispatcher, ok := <-mdp.addDispatcherChan:
			if ok {
				mdp.connectCount++
				mdp.dispatcherList = append(mdp.dispatcherList, dispatcher)
			}
		case dispatcher, ok := <-mdp.delDispatcherChan:
			if ok {
				mdp.breakCount++
				for key, dp := range mdp.dispatcherList {
					if dp == dispatcher {
						mdp.dispatcherList = append(mdp.dispatcherList[0:key], mdp.dispatcherList[key+1:]...)
						break
					}
				}
			}
		}
	}
}
