package ps_dispatcher

import (
	"context"
	"flag"
	"log"
	"os"
	"runtime/pprof"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/go-redis/redis/v8"
)

func TestSingleChannelDispatcher(t *testing.T) {
	var cpuProfile = flag.String("cpuprofile", "single_pprof.prof", "write cpu profile to file")
	flag.Parse()
	if *cpuProfile != "" {
		f, err := os.Create(*cpuProfile)
		if err != nil {
			log.Fatal(err)
		}
		_ = pprof.StartCPUProfile(f)
		defer pprof.StopCPUProfile()
	}

	redisCli := redis.NewClient(&redis.Options{
		Addr:        "127.0.0.1:6379",
		Password:    "",
		DB:          0,
		DialTimeout: time.Second * 5,
	})
	// register dispatcher pool
	dp, err := RegisterSingleChannelDispatcherPool("default", redisCli, "test_channel:*")
	if err != nil {
		t.Fatalf("RegisterSingleChannelDispatcherPool err: %s", err)
	}
	// get dispatcher pool
	dp, err = GetSingleChannelDispatcherPool("default")
	if err != nil {
		t.Fatalf("GetSingleChannelDispatcherPool err: %s", err)
	}

	wg := sync.WaitGroup{}

	p1 := &SingleChannelDispatcher{}
	p1.Init("test_channel:1")
	dp.AddDispatcher(p1)
	go func() {
		wg.Add(1)
		msgCount := 0
		for {
			select {
			case _, ok := <-p1.Channel():
				if ok {
					msgCount++
					//t.Logf("p1 receive: %s", msg)
				} else {
					t.Logf("p1 receive msgCount: %d", msgCount)
					wg.Done()
					return
				}
			}
		}
	}()

	p2 := &SingleChannelDispatcher{}
	p2.Init("test_channel:2")
	dp.AddDispatcher(p2)
	go func() {
		wg.Add(1)
		msgCount := 0
		for {
			select {
			case _, ok := <-p2.Channel():
				if ok {
					msgCount++
					//t.Logf("p2 receive: %s", msg)
				} else {
					t.Logf("p2 receive msgCount: %d", msgCount)
					wg.Done()
					return
				}
			}
		}
	}()

	p3 := &SingleChannelDispatcher{}
	p3.Init("test_channel:3")
	dp.AddDispatcher(p3)
	go func() {
		wg.Add(1)
		msgCount := 0
		for {
			select {
			case _, ok := <-p3.Channel():
				if ok {
					msgCount++
					//t.Logf("p3 receive: %s", msg)
				} else {
					t.Logf("p3 receive msgCount: %d", msgCount)
					wg.Done()
					return
				}
			}
		}
	}()

	p4 := &SingleChannelDispatcher{}
	p4.Init("test_channel:4")
	dp.AddDispatcher(p4)
	go func() {
		wg.Add(1)
		msgCount := 0
		for {
			select {
			case _, ok := <-p4.Channel():
				if ok {
					msgCount++
					//t.Logf("p4 receive: %s", msg)
				} else {
					t.Logf("p4 receive msgCount: %d", msgCount)
					wg.Done()
					return
				}
			}
		}
	}()

	time.Sleep(time.Millisecond * 100)

	for i := 0; i < 100000; i++ {
		pipe := redisCli.Pipeline()
		pipe.Publish(context.Background(), "test_channel:1", 1)
		pipe.Publish(context.Background(), "test_channel:2", 2)
		pipe.Publish(context.Background(), "test_channel:3", 3)
		pipe.Publish(context.Background(), "test_channel:4", 4)
		_, _ = pipe.Exec(context.Background())
	}

	time.Sleep(time.Millisecond * 100)
	dp.DelDispatcher(p1)
	dp.DelDispatcher(p2)
	dp.DelDispatcher(p3)
	dp.DelDispatcher(p4)
	wg.Wait()
}

func TestMultiChannelDispatcher_ModeMulti(t *testing.T) {
	var cpuProfile = flag.String("cpuprofile", "multi_pprof.prof", "write cpu profile to file")
	flag.Parse()
	if *cpuProfile != "" {
		f, err := os.Create(*cpuProfile)
		if err != nil {
			log.Fatal(err)
		}
		_ = pprof.StartCPUProfile(f)
		defer pprof.StopCPUProfile()
	}

	redisCli := redis.NewClient(&redis.Options{
		Addr:        "127.0.0.1:6379",
		Password:    "",
		DB:          0,
		DialTimeout: time.Second * 5,
	})

	wg := sync.WaitGroup{}

	mdp, err := RegisterMultiChannelDispatcherPool("aa", redisCli, DispatcherModeMultiConn)
	if err != nil {
		t.Fatalf("RegisterMultiChannelDispatcherPool err: %s", err)
	}
	mdp.AddProcessFunc(func(msg *redis.Message) (interface{}, error) {
		value, _ := strconv.ParseInt(msg.Payload, 10, 64)
		value++
		return value, err
	})
	mdp, _ = GetMultiChannelDispatcherPool("aa")
	//mdp := NewMultiChannelDispatcherPool(context.Background(), redisCli, DispatcherModeMultiConn)
	//mdp.PrintLength(time.Millisecond * 100)

	md1 := &MultiChannelDispatcher{}
	md1.Init(mdp)
	md1.Subscribe([]string{"test_channel:1", "test_channel:2", "test_channel:3", "test_channel:4", "test_channel:5", "test_channel:6"}...)
	//log.Printf("channel: %s", md1.String())
	//md1.Subscribe([]string{"test_channel:1", "test_channel:2"}...)
	go func() {
		wg.Add(1)
		msgCount := 0
		for {
			select {
			case msg, ok := <-md1.Channel():
				if ok {
					t.Logf("md1 receive msg: %+v", msg)
					msgCount++
				} else {
					t.Logf("md1 receive msgCount: %d", msgCount)
					wg.Done()
					return
				}
			}
		}
	}()

	md2 := &MultiChannelDispatcher{}
	md2.Init(mdp)
	md2.Subscribe([]string{"test_channel:2", "test_channel:3"}...)
	//md2.Subscribe([]string{"test_channel:1", "test_channel:2"}...)
	go func() {
		wg.Add(1)
		msgCount := 0
		for {
			select {
			case msg, ok := <-md2.Channel():
				if ok {
					t.Logf("md2 receive msg: %+v", msg)
					msgCount++
				} else {
					t.Logf("md2 receive msgCount: %d", msgCount)
					wg.Done()
					return
				}
			}
		}
	}()

	md3 := &MultiChannelDispatcher{}
	md3.Init(mdp)
	md3.Subscribe([]string{"test_channel:3", "test_channel:4"}...)
	//md3.Subscribe([]string{"test_channel:1", "test_channel:2"}...)
	go func() {
		wg.Add(1)
		msgCount := 0
		for {
			select {
			case msg, ok := <-md3.Channel():
				if ok {
					t.Logf("md3 receive msg: %+v", msg)
					msgCount++
				} else {
					t.Logf("md3 receive msgCount: %d", msgCount)
					wg.Done()
					return
				}
			}
		}
	}()

	md4 := &MultiChannelDispatcher{}
	md4.Init(mdp)
	md4.Subscribe([]string{"test_channel:4", "test_channel:5"}...)
	//md4.Subscribe([]string{"test_channel:1", "test_channel:2"}...)
	go func() {
		wg.Add(1)
		msgCount := 0
		for {
			select {
			case msg, ok := <-md4.Channel():
				if ok {
					t.Logf("md4 receive msg: %+v", msg)
					msgCount++
				} else {
					t.Logf("md4 receive msgCount: %d", msgCount)
					wg.Done()
					return
				}
			}
		}
	}()

	md5 := &MultiChannelDispatcher{}
	md5.Init(mdp)
	md5.Subscribe([]string{"test_channel:4", "test_channel:5", "test_channel:6"}...)
	//md5.Subscribe([]string{"test_channel:1", "test_channel:2"}...)
	go func() {
		wg.Add(1)
		msgCount := 0
		for {
			select {
			case msg, ok := <-md5.Channel():
				if ok {
					t.Logf("md5 receive msg: %+v", msg)
					msgCount++
				} else {
					t.Logf("md5 receive msgCount: %d", msgCount)
					wg.Done()
					return
				}
			}
		}
	}()

	md1.Unsubscribe("test_channel:1")
	md1.PSubscribe("test_channel:1*")

	// sleep for MultiChannelDispatcher add
	time.Sleep(time.Second)

	for i := 0; i < 10; i++ {
		pipe := redisCli.Pipeline()
		pipe.Publish(context.Background(), "test_channel:1", 1)
		pipe.Publish(context.Background(), "test_channel:2", 2)
		pipe.Publish(context.Background(), "test_channel:3", 3)
		pipe.Publish(context.Background(), "test_channel:4", 4)
		pipe.Publish(context.Background(), "test_channel:5", 5)
		pipe.Publish(context.Background(), "test_channel:6", 6)
		pipe.Publish(context.Background(), "test_channel:12", 12)
		_, _ = pipe.Exec(context.Background())
	}

	// wait for msg receive
	time.Sleep(time.Second)
	mdp.DelDispatcher(md1)
	mdp.DelDispatcher(md2)
	mdp.DelDispatcher(md3)
	mdp.DelDispatcher(md4)
	mdp.DelDispatcher(md5)
	wg.Wait()
}

func TestMultiChannelDispatcher_ModeSingle(t *testing.T) {
	var cpuProfile = flag.String("cpuprofile", "multi_single_mode_pprof.prof", "write cpu profile to file")
	flag.Parse()
	if *cpuProfile != "" {
		f, err := os.Create(*cpuProfile)
		if err != nil {
			log.Fatal(err)
		}
		_ = pprof.StartCPUProfile(f)
		defer pprof.StopCPUProfile()
	}

	redisCli := redis.NewClient(&redis.Options{
		Addr:        "127.0.0.1:6379",
		Password:    "",
		DB:          0,
		DialTimeout: time.Second * 5,
	})

	wg := sync.WaitGroup{}

	mdp := NewMultiChannelDispatcherPool(context.Background(), redisCli, DispatcherModeSingleConn)
	//mdp.PrintLength(time.Millisecond * 100)

	md1 := &MultiChannelDispatcher{}
	md1.Init(mdp)
	mdp.AddDispatcher(md1)
	md1.Subscribe([]string{"test_channel:1", "test_channel:2", "test_channel:3", "test_channel:4", "test_channel:5", "test_channel:6"}...)
	//log.Printf("channel: %s", md1.String())
	//md1.Subscribe([]string{"test_channel:1", "test_channel:2"}...)
	go func() {
		wg.Add(1)
		msgCount := 0
		for {
			select {
			case _, ok := <-md1.Channel():
				if ok {
					msgCount++
				} else {
					t.Logf("md1 receive msgCount: %d", msgCount)
					wg.Done()
					return
				}
			}
		}
	}()

	md2 := &MultiChannelDispatcher{}
	md2.Init(mdp)
	mdp.AddDispatcher(md2)
	md2.Subscribe([]string{"test_channel:2", "test_channel:3"}...)
	//md2.Subscribe([]string{"test_channel:1", "test_channel:2"}...)
	go func() {
		wg.Add(1)
		msgCount := 0
		for {
			select {
			case _, ok := <-md2.Channel():
				if ok {
					msgCount++
				} else {
					t.Logf("md2 receive msgCount: %d", msgCount)
					wg.Done()
					return
				}
			}
		}
	}()

	md3 := &MultiChannelDispatcher{}
	md3.Init(mdp)
	mdp.AddDispatcher(md3)
	md3.Subscribe([]string{"test_channel:3", "test_channel:4"}...)
	//md3.Subscribe([]string{"test_channel:1", "test_channel:2"}...)
	go func() {
		wg.Add(1)
		msgCount := 0
		for {
			select {
			case _, ok := <-md3.Channel():
				if ok {
					msgCount++
				} else {
					t.Logf("md3 receive msgCount: %d", msgCount)
					wg.Done()
					return
				}
			}
		}
	}()

	md4 := &MultiChannelDispatcher{}
	md4.Init(mdp)
	mdp.AddDispatcher(md4)
	md4.Subscribe([]string{"test_channel:4", "test_channel:5"}...)
	//md4.Subscribe([]string{"test_channel:1", "test_channel:2"}...)
	go func() {
		wg.Add(1)
		msgCount := 0
		for {
			select {
			case _, ok := <-md4.Channel():
				if ok {
					msgCount++
				} else {
					t.Logf("md4 receive msgCount: %d", msgCount)
					wg.Done()
					return
				}
			}
		}
	}()

	md5 := &MultiChannelDispatcher{}
	md5.Init(mdp)
	mdp.AddDispatcher(md5)
	md5.Subscribe([]string{"test_channel:4", "test_channel:5", "test_channel:6"}...)
	//md5.Subscribe([]string{"test_channel:1", "test_channel:2"}...)
	go func() {
		wg.Add(1)
		msgCount := 0
		for {
			select {
			case _, ok := <-md5.Channel():
				if ok {
					msgCount++
				} else {
					t.Logf("md5 receive msgCount: %d", msgCount)
					wg.Done()
					return
				}
			}
		}
	}()

	md1.Unsubscribe("test_channel:1")
	md1.PSubscribe("test_channel:1*")

	// sleep for MultiChannelDispatcher add
	time.Sleep(time.Second)

	for i := 0; i < 100000; i++ {
		pipe := redisCli.Pipeline()
		pipe.Publish(context.Background(), "test_channel:1", 1)
		pipe.Publish(context.Background(), "test_channel:2", 2)
		pipe.Publish(context.Background(), "test_channel:3", 3)
		pipe.Publish(context.Background(), "test_channel:4", 4)
		pipe.Publish(context.Background(), "test_channel:5", 5)
		pipe.Publish(context.Background(), "test_channel:6", 6)
		pipe.Publish(context.Background(), "test_channel:12", 12)
		_, _ = pipe.Exec(context.Background())
	}

	// wait for msg receive
	time.Sleep(time.Second)
	mdp.DelDispatcher(md1)
	mdp.DelDispatcher(md2)
	mdp.DelDispatcher(md3)
	mdp.DelDispatcher(md4)
	mdp.DelDispatcher(md5)
	wg.Wait()
}
