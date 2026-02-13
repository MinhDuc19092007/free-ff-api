package main

import (
	"bufio"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

var proxyConfig = struct {
	Sources     []string
	Timeout     time.Duration
	Concurrency int
	OutputFile  string
}{
	Sources: []string{
		"https://api.proxyscrape.com/v2/?request=getproxies&protocol=http&timeout=10000&country=all",
		"https://raw.githubusercontent.com/TheSpeedX/SOCKS-List/master/http.txt",
		"https://raw.githubusercontent.com/clarketm/proxy-list/master/proxy-list-raw.txt",
		"https://raw.githubusercontent.com/monosans/proxy-list/main/proxies/http.txt",
		"https://raw.githubusercontent.com/roosterkid/openproxylist/main/HTTPS_RAW.txt",
	},
	Timeout:     8 * time.Second,
	Concurrency: 500,
	OutputFile:  "proxy.txt",
}

var (
	proxyChecked int64
	proxyWorking int64
	proxyTotal   int64
	proxyMutex   sync.Mutex
	proxyRegex   = regexp.MustCompile(`^\d+\.\d+\.\d+\.\d+:\d+$`)
)

func fetchURL(url string) (string, error) {
	client := &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		},
	}

	resp, err := client.Get(url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	return string(body), nil
}

func downloadProxies(silent bool) []string {
	if !silent {
		fmt.Println("📥 Downloading proxies...\n")
	}

	allProxies := make(map[string]bool)
	var mutex sync.Mutex
	var wg sync.WaitGroup

	for _, source := range proxyConfig.Sources {
		wg.Add(1)
		go func(src string) {
			defer wg.Done()

			data, err := fetchURL(src)
			if err != nil {
				return
			}

			lines := strings.Split(data, "\n")
			var proxies []string
			for _, line := range lines {
				line = strings.TrimSpace(line)
				line = strings.ReplaceAll(line, "\r", "")
				if proxyRegex.MatchString(line) {
					proxies = append(proxies, line)
				}
			}

			mutex.Lock()
			for _, p := range proxies {
				allProxies[p] = true
			}
			mutex.Unlock()

			if !silent {
				shortSrc := src
				if len(src) > 45 {
					shortSrc = src[8:45] + "..."
				}
				fmt.Printf("  ✅ %d from %s\n", len(proxies), shortSrc)
			}
		}(source)
	}

	wg.Wait()

	result := make([]string, 0, len(allProxies))
	for p := range allProxies {
		result = append(result, p)
	}

	if !silent {
		fmt.Printf("\n📊 Total: %d unique proxies\n\n", len(result))
	}

	return result
}

func checkProxy(proxy string) bool {
	parts := strings.Split(proxy, ":")
	if len(parts) != 2 {
		return false
	}

	host := parts[0]
	port := parts[1]

	// Connect to proxy
	conn, err := net.DialTimeout("tcp", net.JoinHostPort(host, port), proxyConfig.Timeout)
	if err != nil {
		return false
	}
	defer conn.Close()

	conn.SetDeadline(time.Now().Add(proxyConfig.Timeout))

	// Send HTTP request through proxy
	request := "GET http://www.google.com/ HTTP/1.1\r\n" +
		"Host: www.google.com\r\n" +
		"User-Agent: Mozilla/5.0\r\n" +
		"Connection: close\r\n\r\n"

	_, err = conn.Write([]byte(request))
	if err != nil {
		return false
	}

	// Read response
	reader := bufio.NewReader(conn)
	response, err := io.ReadAll(reader)
	if err != nil && len(response) == 0 {
		return false
	}

	respStr := string(response)

	// Check if response contains google and status code is OK
	return strings.Contains(respStr, "200") && strings.Contains(strings.ToLower(respStr), "google")
}

func saveProxyToFile(proxy string) {
	proxyMutex.Lock()
	defer proxyMutex.Unlock()

	f, err := os.OpenFile(proxyConfig.OutputFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return
	}
	defer f.Close()

	f.WriteString(proxy + "\n")
}

func proxyWorker(proxies []string, silent bool, wg *sync.WaitGroup) {
	defer wg.Done()

	for _, proxy := range proxies {
		isWorking := checkProxy(proxy)
		atomic.AddInt64(&proxyChecked, 1)

		if isWorking {
			atomic.AddInt64(&proxyWorking, 1)
			saveProxyToFile(proxy)
		}

		if !silent {
			checked := atomic.LoadInt64(&proxyChecked)
			working := atomic.LoadInt64(&proxyWorking)
			total := atomic.LoadInt64(&proxyTotal)
			fmt.Printf("\r🔍 %d/%d | ✅ Live: %d        ", checked, total, working)
		}
	}
}

func RunProxyScraper(silent bool) {
	if !silent {
		fmt.Println(`
╔══════════════════════════════════════════════════╗
║        🔥 LEAK PROXY SCRAPER & CHECKER 🔥        ║
║         Test with real Google request            ║
╚══════════════════════════════════════════════════╝
`)
	}

	// Reset counters
	atomic.StoreInt64(&proxyChecked, 0)
	atomic.StoreInt64(&proxyWorking, 0)

	// Delete old file
	os.Remove(proxyConfig.OutputFile)

	// Download proxies
	proxies := downloadProxies(silent)
	atomic.StoreInt64(&proxyTotal, int64(len(proxies)))

	if len(proxies) == 0 {
		if !silent {
			fmt.Println("❌ No proxies!")
		}
		return
	}

	if !silent {
		fmt.Printf("🚀 Checking with %d concurrent (Google test)...\n\n", proxyConfig.Concurrency)
	}

	// Split proxies into batches
	batchSize := (len(proxies) + proxyConfig.Concurrency - 1) / proxyConfig.Concurrency
	var wg sync.WaitGroup

	for i := 0; i < proxyConfig.Concurrency; i++ {
		start := i * batchSize
		end := start + batchSize
		if start >= len(proxies) {
			break
		}
		if end > len(proxies) {
			end = len(proxies)
		}

		batch := proxies[start:end]
		if len(batch) > 0 {
			wg.Add(1)
			go proxyWorker(batch, silent, &wg)
		}
	}

	wg.Wait()

	if !silent {
		checked := atomic.LoadInt64(&proxyChecked)
		working := atomic.LoadInt64(&proxyWorking)
		fmt.Printf("\n\n════════════════════════════════════════\n")
		fmt.Printf("  Checked: %d | Working: %d\n", checked, working)
		fmt.Printf("  Saved to: %s\n", proxyConfig.OutputFile)
		fmt.Println("════════════════════════════════════════")
	}
}

// Run as standalone
func runProxyMain() {
	silent := false
	for _, arg := range os.Args {
		if arg == "--silent" {
			silent = true
			break
		}
	}
	RunProxyScraper(silent)
}
