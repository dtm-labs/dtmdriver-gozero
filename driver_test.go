package driver

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/zeromicro/go-zero/core/discov"
	"github.com/zeromicro/go-zero/core/proc"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
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
	target := "nacos://127.0.0.1:8848/dtmservice?namespaceId=public&timeoutMs=3000&notLoadCacheAtStart=true&logLevel=debug"
	endpoint := "localhost:36790"
	driver := new(zeroDriver)
	if err := driver.RegisterService(target, endpoint); err != nil {
		t.Errorf("register nacos fail err :%+v", err)
	}

	time.Sleep(60 * time.Second)
}

// The integration tests need real registries. They are skipped by default and
// enabled per registry through environment variables:
//
//	ETCD_ADDR=127.0.0.1:2379 CONSUL_ADDR=127.0.0.1:8500 NACOS_ADDR=127.0.0.1:8848 go test -v ./...
//
// Everything can be brought up locally with docker. Note that nacos needs port
// 9848 published as well: nacos-sdk-go v2 talks gRPC on the main port + 1000,
// and publishing only 8848 fails with "client not connected".
//
//	docker run -d --name etcd -p 2379:2379 quay.io/coreos/etcd:v3.5.21 \
//	  /usr/local/bin/etcd --data-dir=/etcd-data --name node1 \
//	  --listen-client-urls http://0.0.0.0:2379 --advertise-client-urls http://0.0.0.0:2379
//	docker run -d --name consul -p 8500:8500 hashicorp/consul:1.20 agent -dev -client=0.0.0.0
//	docker run -d --name nacos -e MODE=standalone -e NACOS_AUTH_ENABLE=false \
//	  -p 8848:8848 -p 9848:9848 nacos/nacos-server:v2.3.2

const reachedMsg = "reached-real-server"

func envOrSkip(t *testing.T, name string) string {
	t.Helper()
	v := os.Getenv(name)
	if v == "" {
		t.Skipf("%s is not set, skipping integration test", name)
	}
	return v
}

// serveGRPC starts a gRPC server that answers every method with a fixed error,
// so a caller can tell whether its request actually reached the backend.
func serveGRPC(t *testing.T, addr string) {
	t.Helper()
	lis, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatalf("listen %s: %v", addr, err)
	}
	srv := grpc.NewServer(grpc.UnknownServiceHandler(
		func(any, grpc.ServerStream) error {
			return status.Error(codes.Unimplemented, reachedMsg)
		}))
	go srv.Serve(lis)
	t.Cleanup(srv.Stop)
}

// roundTrip resolves the given target and issues a real call through it,
// asserting that service discovery really pointed at the backend.
func roundTrip(t *testing.T, target string) {
	t.Helper()
	d := new(zeroDriver)
	server, method, err := d.ParseServerMethod(target + "/dtmservice.Dtm/Submit")
	if err != nil {
		t.Fatalf("ParseServerMethod(%q): %v", target, err)
	}
	cc, err := grpc.NewClient(server, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("NewClient(%q): %v", server, err)
	}
	defer cc.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	err = cc.Invoke(ctx, method, &emptypb.Empty{}, &emptypb.Empty{}, grpc.WaitForReady(true))
	if st, _ := status.FromError(err); st.Message() != reachedMsg {
		t.Fatalf("backend not reached: server=%q method=%q code=%s msg=%q",
			server, method, st.Code(), st.Message())
	}
}

// ---------------------------------------------------------------- etcd / discov

func TestIntegrationEtcd(t *testing.T) {
	addr := envOrSkip(t, "ETCD_ADDR")
	const endpoint = "127.0.0.1:36801"
	serveGRPC(t, endpoint)
	d := new(zeroDriver)
	d.RegisterAddrResolver()

	// The legacy and the go-zero v1.10 target formats must address the same
	// etcd key in both directions, otherwise registration and resolution drift.
	cases := []struct{ name, register, resolve string }{
		{"legacy format", "etcd://" + addr + "/dtm.legacy", "etcd://" + addr + "/dtm.legacy"},
		{"new format", "etcd:///" + addr + "?key=dtm.new", "etcd:///" + addr + "?key=dtm.new"},
		{"register legacy resolve new", "etcd://" + addr + "/dtm.cross1", "etcd:///" + addr + "?key=dtm.cross1"},
		{"register new resolve legacy", "etcd:///" + addr + "?key=dtm.cross2", "etcd://" + addr + "/dtm.cross2"},
		{"trailing slash", "etcd://" + addr + "/dtm.trail/", "etcd://" + addr + "/dtm.trail"},
		{"nested key", "etcd://" + addr + "/grpc/dtm.nested", "etcd://" + addr + "/grpc/dtm.nested"},
		{"discov legacy format", "discov://" + addr + "/dtm.discov1", "discov://" + addr + "/dtm.discov1"},
		{"discov new format", "discov:///" + addr + "?key=dtm.discov2", "discov:///" + addr + "?key=dtm.discov2"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if err := d.RegisterService(c.register, endpoint); err != nil {
				t.Fatalf("register %q: %v", c.register, err)
			}
			roundTrip(t, c.resolve)
		})
	}
}

// When etcd has authentication enabled, user/password on the target are turned
// into discov.WithPubEtcdAccount. This needs an auth-enabled etcd:
//
//	docker run -d --name etcd-auth -p 23790:2379 quay.io/coreos/etcd:v3.5.21 \
//	  /usr/local/bin/etcd --data-dir=/etcd-data --name node1 \
//	  --listen-client-urls http://0.0.0.0:2379 --advertise-client-urls http://0.0.0.0:2379
//	docker exec etcd-auth etcdctl user add root --new-user-password=rootpw
//	docker exec etcd-auth etcdctl auth enable
//
//	ETCD_AUTH_ADDR=127.0.0.1:23790 ETCD_AUTH_USER=root ETCD_AUTH_PASS=rootpw \
//	  go test -run TestIntegrationEtcdAuth -v ./...
func TestIntegrationEtcdAuth(t *testing.T) {
	addr := envOrSkip(t, "ETCD_AUTH_ADDR")
	user, pass := os.Getenv("ETCD_AUTH_USER"), os.Getenv("ETCD_AUTH_PASS")
	if user == "" || pass == "" {
		t.Skip("ETCD_AUTH_USER / ETCD_AUTH_PASS are not set, skipping")
	}
	const endpoint = "127.0.0.1:36806"
	const key = "dtm.auth"

	target := fmt.Sprintf("etcd://%s/%s?user=%s&password=%s", addr, key, user, pass)
	if err := new(zeroDriver).RegisterService(target, endpoint); err != nil {
		t.Fatalf("register with credentials: %v", err)
	}

	// Read it back with the same credentials to prove the key really landed in
	// the auth-enabled etcd.
	discov.RegisterAccount([]string{addr}, user, pass)
	sub, err := discov.NewSubscriber([]string{addr}, key)
	if err != nil {
		t.Fatalf("NewSubscriber: %v", err)
	}
	defer sub.Close()

	deadline := time.Now().Add(20 * time.Second)
	for {
		for _, v := range sub.Values() {
			if v == endpoint {
				return // passed
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out: %s not found under key %s in auth-enabled etcd (got %v)",
				endpoint, key, sub.Values())
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// ---------------------------------------------------------------- consul

func TestIntegrationConsul(t *testing.T) {
	addr := envOrSkip(t, "CONSUL_ADDR")
	const endpoint = "127.0.0.1:36802"
	serveGRPC(t, endpoint)
	d := new(zeroDriver)
	d.RegisterAddrResolver()

	target := "consul://" + addr + "/dtm-consul"
	if err := d.RegisterService(target, endpoint); err != nil {
		t.Fatalf("register: %v", err)
	}
	if err := waitConsulInstance(addr, "dtm-consul", true, 20*time.Second); err != nil {
		t.Fatal(err)
	}
	roundTrip(t, target)
	// A tagged target must resolve too; that is the branch where the gRPC
	// method ends up inside the query string.
	roundTrip(t, target+"?tag=rpc")
}

// ---------------------------------------------------------------- nacos

func TestIntegrationNacos(t *testing.T) {
	addr := envOrSkip(t, "NACOS_ADDR")
	const endpoint = "127.0.0.1:36803"
	serveGRPC(t, endpoint)
	d := new(zeroDriver)
	d.RegisterAddrResolver()

	const opts = "namespaceId=public&timeoutMs=3000&notLoadCacheAtStart=true&logLevel=info"
	target := "nacos://" + addr + "/dtm-nacos?" + opts
	if err := d.RegisterService(target, endpoint); err != nil {
		t.Fatalf("register: %v", err)
	}
	if err := waitNacosInstance(addr, "dtm-nacos", endpoint, true, 20*time.Second); err != nil {
		t.Fatal(err)
	}
	roundTrip(t, target)

	// notLoadCacheAtStart=false takes the other assignment branch.
	target2 := "nacos://" + addr +
		"/dtm-nacos2?namespaceId=public&timeoutMs=3000&notLoadCacheAtStart=false&logLevel=info"
	if err := d.RegisterService(target2, endpoint); err != nil {
		t.Fatalf("register with notLoadCacheAtStart=false: %v", err)
	}
	if err := waitNacosInstance(addr, "dtm-nacos2", endpoint, true, 20*time.Second); err != nil {
		t.Fatal(err)
	}
}

// ---------------------------------------------------------- deregister on shutdown
//
// proc.Shutdown fires every shutdown listener in the process, so this test is
// placed after the registration tests above.

func TestIntegrationDeregisterOnShutdown(t *testing.T) {
	consulAddr, nacosAddr := os.Getenv("CONSUL_ADDR"), os.Getenv("NACOS_ADDR")
	if consulAddr == "" && nacosAddr == "" {
		t.Skip("neither CONSUL_ADDR nor NACOS_ADDR is set, skipping")
	}
	const endpoint = "127.0.0.1:36804"
	serveGRPC(t, endpoint)
	d := new(zeroDriver)

	if consulAddr != "" {
		if err := d.RegisterService("consul://"+consulAddr+"/dtm-dereg", endpoint); err != nil {
			t.Fatalf("consul register: %v", err)
		}
		if err := waitConsulInstance(consulAddr, "dtm-dereg", true, 20*time.Second); err != nil {
			t.Fatal(err)
		}
	}
	if nacosAddr != "" {
		target := "nacos://" + nacosAddr + "/dtm-dereg?namespaceId=public&timeoutMs=3000&logLevel=info"
		if err := d.RegisterService(target, endpoint); err != nil {
			t.Fatalf("nacos register: %v", err)
		}
		if err := waitNacosInstance(nacosAddr, "dtm-dereg", endpoint, true, 20*time.Second); err != nil {
			t.Fatal(err)
		}
	}

	proc.Shutdown()

	if consulAddr != "" {
		if err := waitConsulInstance(consulAddr, "dtm-dereg", false, 20*time.Second); err != nil {
			t.Errorf("still registered in consul after graceful shutdown: %v", err)
		}
	}
	if nacosAddr != "" {
		if err := waitNacosInstance(nacosAddr, "dtm-dereg", endpoint, false, 20*time.Second); err != nil {
			t.Errorf("still registered in nacos after graceful shutdown: %v", err)
		}
	}
}

// ---------------------------------------------------------- consul TTL expiry
//
// An ungraceful exit (SIGKILL) never runs the deregistration hook, so the
// instance can only be dropped once its TTL check expires. The current
// zero-contrib defaults are TTL=20s and DeregisterCriticalServiceAfter=TTL*3=60s,
// where the pre-upgrade version hardcoded 30s/90s. This test pins that change.

func TestIntegrationConsulTTLExpiry(t *testing.T) {
	addr := envOrSkip(t, "CONSUL_ADDR")
	if testing.Short() {
		t.Skip("skipped in -short mode (this test takes roughly 60-90 seconds)")
	}

	cmd := exec.Command(os.Args[0], "-test.run=TestConsulRegisterHelper", "-test.timeout=5m")
	cmd.Env = append(os.Environ(), "DTM_CONSUL_HELPER=1", "CONSUL_ADDR="+addr)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start helper process: %v", err)
	}
	defer func() { _ = cmd.Process.Kill() }()

	if err := waitConsulInstance(addr, "dtm-ttl", true, 30*time.Second); err != nil {
		t.Fatalf("helper did not register: %v", err)
	}

	// SIGKILL, so the deregistration hook never gets a chance to run.
	if err := cmd.Process.Kill(); err != nil {
		t.Fatalf("kill helper: %v", err)
	}
	_ = cmd.Wait()
	killed := time.Now()

	// These are two different moments and are measured separately:
	//  1. heartbeat stops -> check turns critical -> drops out of the passing
	//     list, which is roughly the TTL (20s now, 30s before);
	//  2. check stays critical for DeregisterCriticalServiceAfter -> removed
	//     from the catalog entirely (TTL*3=60s now, a fixed 90s before).
	if err := waitConsulInstance(addr, "dtm-ttl", false, 2*time.Minute); err != nil {
		t.Fatalf("still in the passing list after the heartbeat stopped: %v", err)
	}
	unhealthy := time.Since(killed)

	if err := waitConsulCatalog(addr, "dtm-ttl", false, 3*time.Minute); err != nil {
		t.Fatalf("still in the catalog after going critical: %v", err)
	}
	deregistered := time.Since(killed)

	// DeregisterCriticalServiceAfter is counted from the moment the check turns
	// critical, not from process death, and consul sweeps critical services on
	// a ~30s interval, so the observed value carries that granularity.
	criticalWindow := deregistered - unhealthy
	t.Logf("after ungraceful exit: unhealthy in %.0fs, deregistered %.0fs later (%.0fs total); "+
		"expected TTL=20s / deregister=60s now, 30s / 90s before",
		unhealthy.Seconds(), criticalWindow.Seconds(), deregistered.Seconds())
	// The TTL leg can be asserted tightly: 20s now vs 30s before, observed ~19s.
	if unhealthy > 25*time.Second {
		t.Errorf("took %.0fs to turn unhealthy, does not match TTL=20s (was 30s)", unhealthy.Seconds())
	}
	// The deregistration leg only gets a loose sanity bound: with the ~30s sweep
	// granularity the current version lands in [60s, 90s] and the old one in
	// [90s, 120s], so a tight bound would be flaky. The log above carries the
	// precise numbers; this only guarantees the instance does get removed.
	if criticalWindow > 150*time.Second {
		t.Errorf("took %.0fs from critical to deregistered, clearly abnormal", criticalWindow.Seconds())
	}
}

// TestConsulRegisterHelper is the child process driven by the test above: it
// registers and then stays alive until it is SIGKILLed.
func TestConsulRegisterHelper(t *testing.T) {
	if os.Getenv("DTM_CONSUL_HELPER") != "1" {
		t.Skip("not the helper subprocess")
	}
	if err := new(zeroDriver).RegisterService(
		"consul://"+os.Getenv("CONSUL_ADDR")+"/dtm-ttl", "127.0.0.1:36805"); err != nil {
		t.Fatalf("helper register: %v", err)
	}
	select {} // block until killed
}

// ---------------------------------------------------------------- registry queries

func waitConsulInstance(addr, service string, want bool, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	var last error
	for {
		got, err := consulHasInstance(addr, service)
		if err == nil && got == want {
			return nil
		}
		last = err
		if time.Now().After(deadline) {
			return fmt.Errorf("timed out waiting for consul service %q present=%v (last err=%v)",
				service, want, last)
		}
		time.Sleep(time.Second)
	}
}

func waitConsulCatalog(addr, service string, want bool, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	var last error
	for {
		body, err := httpGet(fmt.Sprintf("http://%s/v1/catalog/service/%s", addr, url.PathEscape(service)))
		if err == nil {
			var entries []json.RawMessage
			if err = json.Unmarshal(body, &entries); err == nil && (len(entries) > 0) == want {
				return nil
			}
		}
		last = err
		if time.Now().After(deadline) {
			return fmt.Errorf("timed out waiting for %q present=%v in the consul catalog (last err=%v)",
				service, want, last)
		}
		time.Sleep(time.Second)
	}
}

func consulHasInstance(addr, service string) (bool, error) {
	body, err := httpGet(fmt.Sprintf("http://%s/v1/health/service/%s?passing=true",
		addr, url.PathEscape(service)))
	if err != nil {
		return false, err
	}
	var entries []struct {
		Service struct {
			Address string `json:"Address"`
			Port    int    `json:"Port"`
		} `json:"Service"`
	}
	if err := json.Unmarshal(body, &entries); err != nil {
		return false, err
	}
	return len(entries) > 0, nil
}

func waitNacosInstance(addr, service, endpoint string, want bool, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	var last error
	for {
		hosts, err := nacosInstances(addr, service)
		if err == nil {
			found := false
			for _, h := range hosts {
				if fmt.Sprintf("%s:%d", h.IP, h.Port) == endpoint && h.Healthy {
					found = true
					break
				}
			}
			if found == want {
				return nil
			}
		}
		last = err
		if time.Now().After(deadline) {
			return fmt.Errorf("timed out waiting for nacos instance %s present=%v (last err=%v)",
				endpoint, want, last)
		}
		time.Sleep(time.Second)
	}
}

type nacosHost struct {
	IP      string `json:"ip"`
	Port    int    `json:"port"`
	Healthy bool   `json:"healthy"`
}

func nacosInstances(addr, service string) ([]nacosHost, error) {
	body, err := httpGet(fmt.Sprintf("http://%s/nacos/v1/ns/instance/list?serviceName=%s",
		addr, url.QueryEscape(service)))
	if err != nil {
		return nil, err
	}
	var out struct {
		Hosts []nacosHost `json:"hosts"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, err
	}
	return out.Hosts, nil
}

func httpGet(u string) ([]byte, error) {
	resp, err := http.Get(u)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s returned %s: %s", u, resp.Status, strings.TrimSpace(string(body)))
	}
	return body, nil
}

// ---------------------------------------------------------------- unit tests
//
// Everything below runs without any external registry.

func TestGetName(t *testing.T) {
	if got := new(zeroDriver).GetName(); got != DriverName {
		t.Errorf("GetName() = %q, want %q", got, DriverName)
	}
}

func TestRegisterAddrResolver(t *testing.T) {
	// Only checks that repeated calls do not panic; go-zero guards the
	// registration with a sync.Once internally.
	d := new(zeroDriver)
	d.RegisterAddrResolver()
	d.RegisterAddrResolver()
}

// Covers every branch of ParseServerMethod. The returned method must keep its
// leading '/': gRPC expects /pkg.Service/Method and rejects anything else with
// "malformed method name".
func TestParseServerMethod(t *testing.T) {
	tests := []struct {
		name   string
		uri    string
		server string
		method string
	}{
		// --- etcd / discov, legacy format ---
		{"etcd legacy", "etcd://localhost:2379/dtmservice/dtmservice.Dtm/Submit",
			"etcd://localhost:2379/dtmservice", "/dtmservice.Dtm/Submit"},
		{"etcd legacy multi host", "etcd://h1:2379,h2:2379,h3:2379/dtmservice/dtmservice.Dtm/Submit",
			"etcd://h1:2379,h2:2379,h3:2379/dtmservice", "/dtmservice.Dtm/Submit"},
		{"discov legacy", "discov://localhost:2379/dtmservice/dtmservice.Dtm/Submit",
			"discov://localhost:2379/dtmservice", "/dtmservice.Dtm/Submit"},

		// --- etcd / discov, format introduced in go-zero v1.10 ---
		{"etcd new", "etcd:///localhost:2379?key=dtmservice/dtmservice.Dtm/Submit",
			"etcd:///localhost:2379?key=dtmservice", "/dtmservice.Dtm/Submit"},
		{"etcd new multi host", "etcd:///h1:2379,h2:2379,h3:2379?key=dtmservice/dtmservice.Dtm/Submit",
			"etcd:///h1:2379,h2:2379,h3:2379?key=dtmservice", "/dtmservice.Dtm/Submit"},
		{"discov new", "discov:///localhost:2379?key=dtmservice/dtmservice.Dtm/Submit",
			"discov:///localhost:2379?key=dtmservice", "/dtmservice.Dtm/Submit"},
		{"etcd new escaped key", "etcd:///localhost:2379?key=%2Fgrpc%2Fmy-service/pkg.Service/Method",
			"etcd:///localhost:2379?key=%2Fgrpc%2Fmy-service", "/pkg.Service/Method"},

		// --- consul ---
		{"consul with tag", "consul://127.0.0.1:8500/grpc-product?tag=wtm_service_grpc_q/product.Product/IntegralProdStockDeduction",
			"consul://127.0.0.1:8500/grpc-product?tag=wtm_service_grpc_q", "/product.Product/IntegralProdStockDeduction"},
		{"consul no query", "consul://192.168.1.10:8500/order-service/order.OrderService/CreateOrder",
			"consul://192.168.1.10:8500/order-service", "/order.OrderService/CreateOrder"},
		{"consul multiple params", "consul://10.0.0.5:8500/inventory-svc?tag=prod&token=xyz789&timeout=5s/inventory.StockService/DeductStock",
			"consul://10.0.0.5:8500/inventory-svc?tag=prod&token=xyz789&timeout=5s", "/inventory.StockService/DeductStock"},
		{"consul simple method", "consul://localhost:8500/my-api-gateway/hello.World/Say",
			"consul://localhost:8500/my-api-gateway", "/hello.World/Say"},
		{"consul ipv6", "consul://[::1]:8500/cache-svc?region=local/cache.Redis/Get",
			"consul://[::1]:8500/cache-svc?region=local", "/cache.Redis/Get"},
		{"consul valueless query", "consul://consul.local:8500/auth-svc?debug/auth.Service/Login",
			"consul://consul.local:8500/auth-svc?debug", "/auth.Service/Login"},

		// --- nacos, which goes through the query-stripping branch ---
		{"nacos with query", "nacos://127.0.0.1:8848/dtmservice?namespaceId=public&timeoutMs=3000/dtmservice.Dtm/Submit",
			"nacos://127.0.0.1:8848/dtmservice", "/dtmservice.Dtm/Submit"},
		{"nacos no query", "nacos://127.0.0.1:8848/dtmservice/dtmservice.Dtm/Submit",
			"nacos://127.0.0.1:8848/dtmservice", "/dtmservice.Dtm/Submit"},

		// --- direct connection without a scheme ---
		{"no scheme", "localhost:36790/dtmservice.Dtm/Submit",
			"localhost:36790", "/dtmservice.Dtm/Submit"},
	}

	d := new(zeroDriver)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server, method, err := d.ParseServerMethod(tt.uri)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if server != tt.server {
				t.Errorf("server = %q, want %q", server, tt.server)
			}
			if method != tt.method {
				t.Errorf("method = %q, want %q", method, tt.method)
			}
			if !strings.HasPrefix(method, "/") {
				t.Errorf("method %q has no leading '/', gRPC would reject it as a malformed method name", method)
			}
		})
	}
}

func TestParseServerMethodErrors(t *testing.T) {
	tests := []struct{ name, uri string }{
		{"no scheme and no slash", "localhost:36790"},
		{"consul without method", "consul://127.0.0.1:8500/dtmservice"},
		{"consul query without method", "consul://127.0.0.1:8500/dtmservice?tag=a"},
		{"etcd new format without method", "etcd:///127.0.0.1:2379?key=dtmservice"},
		{"discov new format without method", "discov:///127.0.0.1:2379?key=dtmservice"},
		{"nacos query without method", "nacos://127.0.0.1:8848/dtmservice?namespaceId=public"},
		// the various url.Parse failures
		{"consul invalid port", "consul://h:abc/svc/m.S/M"},
		{"consul empty path", "consul://127.0.0.1:8500"},
		{"etcd new format invalid character", "etcd:///h\x7f?key=a/b"},
		{"nacos invalid port", "nacos://h:abc/svc/m.S/M"},
		{"etcd legacy invalid port", "etcd://h:abc/svc/m.S/M"},
	}
	d := new(zeroDriver)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, _, err := d.ParseServerMethod(tt.uri); err == nil {
				t.Errorf("ParseServerMethod(%q) = nil error, want error", tt.uri)
			}
		})
	}
}

// Covers the registration-side target parsing, which mirrors targets.GetHosts
// and targets.GetKey in go-zero's resolver.
func TestParseEtcdTarget(t *testing.T) {
	tests := []struct {
		name   string
		target string
		hosts  []string
		key    string
	}{
		{"legacy single host", "etcd://localhost:2379/dtmservice", []string{"localhost:2379"}, "dtmservice"},
		{"legacy multi host", "etcd://h1:2379,h2:2379/dtmservice", []string{"h1:2379", "h2:2379"}, "dtmservice"},
		{"legacy trailing slash", "etcd://localhost:2379/dtmservice/", []string{"localhost:2379"}, "dtmservice"},
		{"legacy nested key", "etcd://localhost:2379/grpc/dtmservice", []string{"localhost:2379"}, "grpc/dtmservice"},
		{"legacy with account", "etcd://localhost:2379/dtmservice?user=root&password=x", []string{"localhost:2379"}, "dtmservice"},
		{"legacy empty host segment", "etcd://h1:2379,,h2:2379/dtmservice", []string{"h1:2379", "h2:2379"}, "dtmservice"},
		{"new single host", "etcd:///localhost:2379?key=dtmservice", []string{"localhost:2379"}, "dtmservice"},
		{"new multi host", "etcd:///h1:2379,h2:2379,h3:2379?key=dtmservice", []string{"h1:2379", "h2:2379", "h3:2379"}, "dtmservice"},
		{"new escaped key", "etcd:///localhost:2379?key=%2Fgrpc%2Fmy-service", []string{"localhost:2379"}, "/grpc/my-service"},
		{"new with account", "etcd:///localhost:2379?key=dtmservice&user=root&password=x", []string{"localhost:2379"}, "dtmservice"},
		{"new without key", "etcd:///localhost:2379", []string{"localhost:2379"}, ""},
		{"new without host", "etcd:///?key=dtmservice", nil, "dtmservice"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			u, err := url.Parse(tt.target)
			if err != nil {
				t.Fatalf("parse target: %v", err)
			}
			query, _ := url.ParseQuery(u.RawQuery)
			hosts, key := parseEtcdTarget(u, query)
			if len(hosts) != len(tt.hosts) {
				t.Fatalf("hosts = %v, want %v", hosts, tt.hosts)
			}
			for i := range hosts {
				if hosts[i] != tt.hosts[i] {
					t.Errorf("hosts[%d] = %q, want %q", i, hosts[i], tt.hosts[i])
				}
			}
			if key != tt.key {
				t.Errorf("key = %q, want %q", key, tt.key)
			}
		})
	}
}

// Covers the RegisterService branches that need no external registry.
func TestRegisterServiceLocalBranches(t *testing.T) {
	d := new(zeroDriver)

	t.Run("empty target returns nil", func(t *testing.T) {
		if err := d.RegisterService("", "localhost:36790"); err != nil {
			t.Errorf("empty target should return nil, got %v", err)
		}
	})

	errCases := []struct{ name, target string }{
		{"malformed url", "://bad"},
		{"unknown scheme", "zookeeper://127.0.0.1:2181/dtmservice"},
		{"etcd legacy without key", "etcd://localhost:2379"},
		{"etcd new format without key", "etcd:///localhost:2379"},
		{"etcd new format without host", "etcd:///?key=dtmservice"},
		{"discov without key", "discov://localhost:2379"},
	}
	for _, tt := range errCases {
		t.Run(tt.name, func(t *testing.T) {
			if err := d.RegisterService(tt.target, "localhost:36790"); err == nil {
				t.Errorf("RegisterService(%q) = nil, want error", tt.target)
			}
		})
	}
}
