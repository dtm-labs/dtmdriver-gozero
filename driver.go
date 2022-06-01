package driver

import (
	"fmt"
	"github.com/nacos-group/nacos-sdk-go/common/constant"
	"github.com/zeromicro/zero-contrib/zrpc/registry/nacos"
	"net/url"
	"strconv"
	"strings"

	"github.com/zeromicro/zero-contrib/zrpc/registry/consul"

	"github.com/dtm-labs/dtmdriver"
	"github.com/zeromicro/go-zero/core/discov"
	"github.com/zeromicro/go-zero/zrpc/resolver"
)

const (
	DriverName = "dtm-driver-gozero"
	kindEtcd   = "etcd"
	kindDiscov = "discov"
	kindConsul = "consul"
	kindNacos  = "nacos"
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

	opts := make([]discov.PubOption, 0)
	query, _ := url.ParseQuery(u.RawQuery)
	if query.Get("user") != "" {
		opts = append(opts, discov.WithPubEtcdAccount(query.Get("user"), query.Get("password")))
	}

	switch u.Scheme {
	case kindDiscov:
		fallthrough
	case kindEtcd:
		pub := discov.NewPublisher(strings.Split(u.Host, ","), strings.TrimPrefix(u.Path, "/"), endpoint, opts...)
		pub.KeepAlive()
	case kindConsul:
		return consul.RegisterService(endpoint, consul.Conf{
			Host: u.Host,
			Key:  strings.TrimPrefix(u.Path, "/"),
			Tag:  []string{"tag", "rpc"},
			Meta: map[string]string{
				"Protocol": "grpc",
			},
		})
	case kindNacos:
		// server
		hostPort := strings.Split(u.Host, ":")
		host := hostPort[0]
		port, _ := strconv.ParseUint(hostPort[1], 10, 64)

		// client
		var namespaceId = "public"
		var timeoutMs uint64 = 5000
		var notLoadCacheAtStart = true
		var logLevel = "debug"
		if query.Get("namespaceId") != "" {
			namespaceId = query.Get("namespaceId")
		}
		if query.Get("timeoutMs") != "" {
			timeoutMs, _ = strconv.ParseUint(query.Get("timeoutMs"), 10, 64)
		}
		if query.Get("notLoadCacheAtStart") != "" && query.Get("notLoadCacheAtStart") == "false" {
			notLoadCacheAtStart = false
		}
		if query.Get("logLevel") != "" {
			logLevel = query.Get("logLevel")
		}

		sc := []constant.ServerConfig{
			*constant.NewServerConfig(host, port),
		}
		cc := &constant.ClientConfig{
			NamespaceId:         namespaceId,
			TimeoutMs:           timeoutMs,
			NotLoadCacheAtStart: notLoadCacheAtStart,
			LogLevel:            logLevel,
		}
		opts := nacos.NewNacosConfig(strings.TrimPrefix(u.Path, "/"), endpoint, sc, cc)
		return nacos.RegisterService(opts)
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
	//resolve gozero consul wait=xx url.Parse no standard
	if (strings.Contains(uri, kindConsul) || strings.Contains(uri, kindNacos)) && strings.Contains(uri, "?") {
		tmp := strings.Split(uri, "?")
		sep := strings.IndexByte(tmp[1], '/')
		if sep == -1 {
			return "", "", fmt.Errorf("bad url: '%s'. no '/' found", uri)
		}
		uri = tmp[0] + tmp[1][sep:]
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
