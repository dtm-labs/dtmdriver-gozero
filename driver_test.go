package driver

import (
	"testing"
	"time"
)

func TestZeroDriver_RegisterGrpcService(t *testing.T) {

	// consul
	//target := "consul://127.0.0.1:8500/dtmservice"
	//endpoint := "localhost:36790"
	//driver := new(zeroDriver)
	//if err := driver.RegisterGrpcService(target, endpoint); err != nil {
	//	t.Errorf("register consul fail err :%+v", err)
	//}

	// nacos
	target := "nacos://172.16.130.211:8848/dtmservice?namespaceId=public&timeoutMs=3000&notLoadCacheAtStart=true&logLevel=debug"
	endpoint := "localhost:36790"
	driver := new(zeroDriver)
	if err := driver.RegisterGrpcService(target, endpoint); err != nil {
		t.Errorf("register consul fail err :%+v", err)
	}

	time.Sleep(60 * time.Second)
}
