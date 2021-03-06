package etcdkv

import (
	"context"
	"fmt"
	"github.com/coreos/etcd/clientv3"
	"github.com/coreos/etcd/mvcc/mvccpb"
	"log"
	"sync"
	"time"
)

type Watcher struct {
	opt     *watcherOption
	ctx     context.Context
	cancel  context.CancelFunc
	watchCh clientv3.WatchChan
	wait    *sync.WaitGroup
	ticker  *time.Ticker
	closed  bool
}

func NewWatcher(opts ...WatcherOption) *Watcher {

	opt := &watcherOption{
		ttl:      time.Minute * 10,
		resolver: &PrintWatchKvResolver{},
	}

	for _, optFun := range opts {
		optFun(opt)
	}

	if opt.client == nil {
		watcherErrorHandler(fmt.Errorf("client is empty"))
		return nil
	}

	wait, ticker := &sync.WaitGroup{}, time.NewTicker(opt.ttl)
	ctx, cancel := context.WithCancel(context.Background())

	return &Watcher{opt: opt, ctx: ctx, cancel: cancel, wait: wait, ticker: ticker}
}

func (w *Watcher) Start() {
	// First Get The Namespace
	w.fromNamespace()
	w.wait.Add(1)
	go func() {
		defer w.wait.Done()
		w.start()
	}()
}

func (w *Watcher) Close() {
	w.close()
}

func (w *Watcher) start() {
retry:
	w.watch()
	for {
		select {
		case change, ok := <-w.watchCh: // 监听变化
			if (!ok || change.Err() != nil) && !w.closed {
				err := fmt.Errorf("%v, watch chan closed, retry again 3 second later", change.Err())
				watcherErrorHandler(err)
				time.Sleep(time.Second * 3)
				log.Printf("etcdkv watcher start retry ... \n")
				goto retry
			}
			for _, event := range change.Events {
				kv := event.Kv
				if kv == nil {
					continue
				}
				k, v, putTime := w.parseKv(kv.Key, kv.Value)
				switch event.Type {
				case mvccpb.PUT:
					w.opt.resolver.Put(event.Kv.String(), w.opt.namespace, string(k), string(v), putTime, kv.Version)
				case mvccpb.DELETE:
					w.opt.resolver.Del(event.Kv.String(), w.opt.namespace, string(k), string(v), putTime, kv.Version)
				}
			}
		case <-w.ticker.C: // 定时拉取
			w.fromNamespace()
		case <-w.ctx.Done(): // 监听关闭
			log.Println("etcdkv watcher context is done")
			w.closeClient()
			w.closeTicker()
			return
		}
	}
}

func (w *Watcher) close() {
	w.closed = true
	w.cancel()
	w.wait.Wait()
}

func (w *Watcher) watch() {
	w.watchCh = w.opt.client.Watch(w.ctx, w.opt.sepNamespace, clientv3.WithPrefix(), clientv3.WithPrevKV())
}

func (w *Watcher) fromNamespace() {
	if response, err := w.opt.client.Get(w.ctx, w.opt.sepNamespace, clientv3.WithPrefix()); err != nil {
		watcherErrorHandler(err)
	} else {
		for _, kv := range response.Kvs {
			k, v, putTime := w.parseKv(kv.Key, kv.Value)
			w.opt.resolver.Get(kv.String(), w.opt.namespace, string(k), string(v), putTime, kv.Version)
		}
	}
}

// 从key中去除namespace;key格式=/namespace/key
func (w *Watcher) parseKv(key, value []byte) (k []byte, v []byte, putTime int64) {

	k, v = key, value

	// parse key
	if lenNamespace := len([]byte(w.opt.sepNamespace)); len(key) > lenNamespace {
		k = key[lenNamespace:]
	}

	return
}

func (w *Watcher) closeClient() {
	if err := w.opt.client.Close(); err != nil {
		watcherErrorHandler(err)
	}
}

func (w *Watcher) closeTicker() {
	w.ticker.Stop()
}
