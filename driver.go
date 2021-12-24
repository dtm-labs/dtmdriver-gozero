package driver

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/dtm-labs/dtmdriver"
	"github.com/tal-tech/go-zero/core/discov"
	"github.com/tal-tech/go-zero/zrpc/resolver"
)

const (
	DriverName = "dtm-driver-gozero"
	kindEtcd   = "etcd"
	kindDiscov = "discov"
)

type (
	zeroDriver struct{}
)

func (z *zeroDriver) GetName() string {
	return DriverName
}

func (z *zeroDriver) RegisterGrpcResolver() {
	resolver.Register()
}

func (z *zeroDriver) RegisterGrpcService(target string, endpoint string) error {
	if target == "" { // empty target, no action
		return nil
	}
	u, err := url.Parse(target)
	if err != nil {
		return err
	}

	switch u.Scheme {
	case kindDiscov:
		fallthrough
	case kindEtcd:
		pub := discov.NewPublisher(strings.Split(u.Host, ","), strings.TrimPrefix(u.Path, "/"), endpoint)
		pub.KeepAlive()
	default:
		return fmt.Errorf("unknown scheme: %s", u.Scheme)
	}

	return nil
}

func (z *zeroDriver) ParseServerMethod(uri string) (server string, method string, err error) {
	if !strings.Contains(uri, "//") { // 处理无scheme的情况，如果您没有直连，可以不处理
		sep := strings.IndexByte(uri, '/')
		if sep == -1 {
			return "", "", fmt.Errorf("bad url: '%s'. no '/' found", uri)
		}
		return uri[:sep], uri[sep:], nil

	}
	u, err := url.Parse(uri)
	if err != nil {
		return "", "", nil
	}
	index := strings.IndexByte(u.Path[1:], '/') + 1
	return u.Scheme + "://" + u.Host + u.Path[:index], u.Path[index:], nil
}

func init() {
	dtmdriver.Register(&zeroDriver{})
}
