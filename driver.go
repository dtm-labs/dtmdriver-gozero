package driver

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/nacos-group/nacos-sdk-go/common/constant"
	"github.com/zeromicro/zero-contrib/zrpc/registry/nacos"

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

func (z *zeroDriver) RegisterAddrResolver() {
	resolver.Register()
}

func (z *zeroDriver) RegisterService(target string, endpoint string) error {
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

	// 单独处理consul 的target
	// 1. 原始标准（带 tag）
	//"consul://127.0.0.1:8500/grpc-product?tag=wtm_service_grpc_q/product.Product/IntegralProdStockDeduction",
	// 2. 无 query，path 中带方法（关键修正用例）
	//"consul://192.168.1.10:8500/order-service/order.OrderService/CreateOrder",
	// 3. 多参数（含 &）
	//"consul://10.0.0.5:8500/inventory-svc?tag=prod&token=xyz789&timeout=5s&zone=us-east-1/inventory.StockService/DeductStock",
	// 4. 简单方法名
	//"consul://localhost:8500/my-api-gateway/hello.World/Say",
	// 5. IPv6
	//"consul://[::1]:8500/cache-svc?region=local/cache.Redis/Get",
	// 6. query 中只有前缀和方法
	//"consul://consul.local:8500/auth-svc?debug/auth.Service/Login",
	// 7. 新增：显式包含多个 & 的复杂查询参数 
	//"consul://172.16.0.100:8500/payment-svc?env=prod&tag=grpc_q&a=b&debug=true/payment.PaymentService/Process",
	if strings.Contains(uri, kindConsul) {
		u, err := url.Parse(uri)
		if err != nil {
			return "", "", err
		}

		var method string

		if u.RawQuery != "" {
			// With query: split at FIRST '/' in RawQuery
			if i := strings.Index(u.RawQuery, "/"); i >= 0 {
				method = u.RawQuery[i+1:]
				u.RawQuery = u.RawQuery[:i]
			}
		} else {
			// Without query: service name is the FIRST path segment
			path := u.Path
			if len(path) == 0 || path[0] != '/' {
				return "", "", fmt.Errorf("invalid path")
			}
			if idx := strings.Index(path[1:], "/"); idx >= 0 {
				splitPos := 1 + idx
				method = path[splitPos+1:]
				u.Path = path[:splitPos]
			}
		}

		if method == "" {
			return "", "", fmt.Errorf("gRPC method part missing or empty")
		}
		return u.String(), method, nil
	}
	
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
