// Command webone is WebOne, the HTTP proxy that lets old web browsers
// reach today's web, ported to Go.
//
//	webone [config file] [port] [-cfg file] [-l logfile|no] [-p port]
//	       [-h hostname] [-t tempdir] [-a user:pass] [--dump [file]]
//	       [--rebuild-ca] [--daemon] [--help]
package main

import (
	"errors"
	"fmt"
	"net"
	"os"
	"os/signal"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/D-Modem/webone-go/internal/certs"
	"github.com/D-Modem/webone-go/internal/config"
	"github.com/D-Modem/webone-go/internal/logx"
	"github.com/D-Modem/webone-go/internal/proxy"
)

type arg struct{ name, value string }

const unnamed = "--wo-short"

// parseArgs splits the command line as WebOne does: "-name value" pairs,
// and values without a name (a port or a configuration file).
func parseArgs(args []string) []arg {
	var out []arg
	name, value, lastWasValue := unnamed, "", false
	for _, a := range args {
		if strings.HasPrefix(a, "-") || (runtime.GOOS == "windows" && strings.HasPrefix(a, "/")) {
			out = append(out, arg{name, value})
			name, value, lastWasValue = a, "", false
			continue
		}
		if lastWasValue {
			out = append(out, arg{name, value})
		}
		value, lastWasValue = a, true
	}
	return append(out, arg{name, value})
}

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(argv []string) int {
	version := proxy.Version
	system := runtime.GOOS + " " + runtime.GOARCH + " (Go " + runtime.Version() + ")"
	fmt.Printf("WebOne HTTP Proxy Server %s\nhttps://github.com/D-Modem/webone-go (a Go port of https://github.com/atauenis/webone)\n\n\n", version)

	args := parseArgs(argv)
	var customConfig, logFile string
	logSet, logOff, rebuildCA, daemon := false, false, false, false
	port := -1
	for _, a := range args {
		switch a.name {
		case "/cfg", "-cfg", "-config", "--config":
			customConfig = a.value
		case "/l", "-l", "--log":
			logSet = true
			if a.value == "" || a.value == "no" {
				logOff = true
			} else {
				logFile = a.value
			}
		case "/t", "-t", "--tmp", "--temp", "/p", "-p", "--port", "--http-port", "--https-port", "--ftp-port",
			"/h", "-h", "--host", "--hostname", "/a", "-a", "--proxy-authenticate",
			"--dump", "--dump-headers", "--dump-requests":
			// after the configuration is loaded
		case "--rebuild-ca":
			rebuildCA = true
		case "--daemon":
			daemon = true
		case "--help", "-?", "/?":
			fmt.Println("Usage: webone [config file] [port] [options]")
			fmt.Println()
			fmt.Println("  -cfg FILE        configuration file (default: webone.conf, searched for)")
			fmt.Println("  -l FILE | -l no  log file, or no log file")
			fmt.Println("  -p PORT          port to listen on")
			fmt.Println("  -h NAME          host name of the proxy as clients reach it")
			fmt.Println("  -t DIR           directory for temporary files")
			fmt.Println("  -a USER:PASS     require this proxy login (replaces the configured ones)")
			fmt.Printf("  --dump [FILE]    save all HTTP traffic (default dump-%%Url%%.log)\n")
			fmt.Println("  --rebuild-ca     make a new CA certificate, then exit")
			fmt.Println("  --daemon         print nothing; log to the log file only")
			fmt.Println()
			fmt.Println("All of WebOne's options are described in its wiki: https://github.com/atauenis/webone/wiki")
			fmt.Println()
			fmt.Println("Initially made by Alexander Tauenis. Moscow, Russian Federation.")
			return 0
		case unnamed:
			if strings.TrimSpace(a.value) == "" {
				break
			}
			if n, err := strconv.Atoi(a.value); err == nil {
				port = n
				fmt.Printf("Using custom port %d.\n", port)
				break
			}
			customConfig = a.value
		default:
			fmt.Printf("Unknown command line argument: %s.\n", a.name)
		}
	}

	cfg := config.New()
	cfg.Vars.Set("WOVer", version)
	cfg.Vars.Set("WOSystem", system)
	if customConfig != "" {
		customConfig = cfg.Expand(customConfig, nil)
	}
	if logFile != "" {
		logx.OpenLogFile(cfg.Expand(logFile, nil), false, version, argv)
	}

	cfgFile, err := config.FindConfigFile(customConfig)
	if err == nil {
		if err = cfg.LoadFile(cfgFile); err == nil {
			err = cfg.Process()
		}
	}
	if err != nil {
		fmt.Printf("Error while loading configuration: %v\n", err)
		logx.Server(false, "WebOne has been exited due to lack of configuration.")
		return 1
	}
	if port > 0 {
		cfg.Port = port
	}
	if !logSet && cfg.LogFile != "" {
		logx.OpenLogFile(cfg.Expand(cfg.LogFile, nil), cfg.LogFileAppend, version, argv)
	}

	// options that override the configuration
	for _, a := range args {
		switch a.name {
		case "/t", "-t", "--tmp", "--temp":
			cfg.TemporaryDirectory = cfg.Expand(a.value, nil)
		case "/p", "-p", "--port", "--http-port", "--https-port", "--ftp-port":
			n, err := strconv.Atoi(a.value)
			if err != nil {
				fmt.Printf("Warning: Wrong argument '%s %s': %v.\n", a.name, a.value, err)
				continue
			}
			cfg.Port = n
		case "/h", "-h", "--host", "--hostname":
			cfg.DefaultHostName = a.value
		case "/a", "-a", "--proxy-authenticate":
			cfg.Authenticate = []string{a.value}
		case "--dump", "--dump-headers", "--dump-requests":
			file := "dump-%Url%.log"
			if a.value != "" {
				file = a.value
			}
			logx.Server(true, "Will save all HTTP traffic to: %s.", file)
			sec, _ := config.ParseSection("[Edit]", "[command line]")
			opt, _ := config.ParseOption("AddDumping="+file, "[command line]")
			sec.Options = append(sec.Options, opt)
			if set, err := config.NewEditSet(sec, cfg); err == nil {
				cfg.EditRules = append(cfg.EditRules, set)
			}
		}
	}
	cfg.SetProxyVars()

	if logOff || (!logSet && cfg.LogFile == "") {
		logx.OpenLogFile("", false, version, argv)
	}
	if daemon {
		if !logx.Enabled() {
			fmt.Println("Error: log file is not available, please fix the problem. Exiting.")
			return 1
		}
		fmt.Println("The proxy runs in daemon mode. See all messages in the log file.")
		logx.Daemon = true
	}

	pacDefault := cfg.PAC == ""
	pac := config.DefaultPACHeader + config.DefaultPACHttp
	protocols := "HTTP 1.1"

	var ca, root *certs.CA
	if cfg.SslEnable {
		ca, root, err = loadCA(cfg, rebuildCA)
		if rebuildCA {
			if err != nil {
				logx.Server(true, "Unable to create CA Certificate: %v.", err)
				return 1
			}
			return 0
		}
		if err != nil {
			logx.Server(true, "%v.", err)
			cfg.SslEnable = false
		} else {
			protocols += ", HTTPS 1.1"
			pac += config.DefaultPACHttps
		}
		if cfg.SslSiteCertGenerator != "" && cfg.SslSiteCerts == "" {
			logx.Server(true, "Warning: `SslSiteCertGenerator` is set but `SslSiteCerts` is not. Will use internal certificate generator.")
			cfg.SslSiteCertGenerator = ""
		}
	}
	protocols += ", CERN-compatible"
	pac += config.DefaultPACFtp + config.DefaultPACFooter
	if pacDefault {
		cfg.PAC = pac
	}
	logx.Server(false, "Configured to http://%s:%d/, %s", cfg.DefaultHostName, cfg.Port, protocols)

	srv, err := proxy.New(cfg, ca, root)
	if err != nil {
		logx.Server(true, "Server initialize failed: %v", err)
		return 1
	}

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sig
		srv.Stop()
	}()

	addr := net.JoinHostPort("", strconv.Itoa(cfg.Port))
	if cfg.IP != nil {
		addr = net.JoinHostPort(cfg.IP.String(), strconv.Itoa(cfg.Port))
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		logx.Server(true, "Cannot start server: %v", err)
		if errors.Is(err, syscall.EACCES) && runtime.GOOS != "windows" {
			fmt.Println()
			fmt.Println(`You need to use "sudo webone" or use Port greater than 1024.`)
		}
		logx.Close()
		return 1
	}
	logx.Server(true, "Listening for HTTP 1.x on port %d.", cfg.Port)
	if err := srv.Serve(ln); err != nil {
		logx.Server(true, "Server stopped: %v", err)
		logx.Close()
		return 1
	}
	logx.Server(false, "WebOne has been exited.")
	logx.Close()
	return 0
}

// loadCA is the CA that signs site certificates and the root clients
// install: the D-Modem BBS's (SslCaDirectory), or WebOne's own, made when
// missing (or always with --rebuild-ca).
func loadCA(cfg *config.Config, rebuild bool) (*certs.CA, *certs.CA, error) {
	if cfg.SslCaDirectory != "" {
		if rebuild {
			return nil, nil, fmt.Errorf("the CA in %s belongs to the BBS (bbs ca); it is not rebuilt here", cfg.SslCaDirectory)
		}
		ca, root, err := certs.BBSCA(cfg.SslCaDirectory)
		if err != nil {
			return nil, nil, fmt.Errorf("Unable to load CA Certificate: %v", err)
		}
		if !cfg.SslHashAlgorithmSet {
			// sign as the CA above does: a SHA-1 root is for clients that
			// know no better
			cfg.SslHashAlgorithm = certs.HashOf(ca.Cert)
		}
		if ca == root {
			logx.Server(true, "Using as SSL Certificate Authority: %s (the BBS root CA itself; better give the proxy a sub-CA of its own with \"bbs ca subca proxy\").", cfg.SslCaDirectory)
		} else {
			logx.Server(false, "Using as SSL Certificate Authority: %s, sub-CA \"%s\" of \"%s\".",
				cfg.SslCaDirectory, ca.Cert.Subject.CommonName, root.Cert.Subject.CommonName)
		}
		checkValidity(cfg, ca)
		return ca, root, nil
	}

	const minPEM = 52 // header and footer only
	have := false
	if st, err := os.Stat(cfg.SslCertificate); err == nil && st.Size() > minPEM {
		if st, err := os.Stat(cfg.SslPrivateKey); err == nil && st.Size() > minPEM {
			have = true
		}
	}
	if have && !rebuild {
		logx.Server(false, "Using as SSL Certificate Authority: %s, %s.", cfg.SslCertificate, cfg.SslPrivateKey)
	} else {
		if rebuild {
			fmt.Println()
			logx.Server(true, "CA Certificate will be new, so import it to browser(s) after build succeeds.")
		}
		logx.Server(true, "Creating root SSL Certificate & Private Key for CA...")
		if err := certs.CreateCA(cfg.SslCertificate, cfg.SslPrivateKey, cfg.SslRootSubject, cfg.SslHashAlgorithm,
			cfg.SslRootValidAfter, cfg.SslRootValidBefore); err != nil {
			return nil, nil, fmt.Errorf("Unable to create CA Certificate: %v", err)
		}
		logx.Server(true, "CA Certificate: %s;   Key: %s.", cfg.SslCertificate, cfg.SslPrivateKey)
	}
	ca, err := certs.LoadCA(cfg.SslCertificate, cfg.SslPrivateKey)
	if err != nil {
		return nil, nil, fmt.Errorf("Unable to load CA Certificate: %v", err)
	}
	if rebuild {
		logx.Server(true, "The new certificate is called \"%s\".", ca.Cert.Subject.CommonName)
		logx.Server(true, "WebOne will now exit.")
		return ca, ca, nil
	}
	checkValidity(cfg, ca)
	return ca, ca, nil
}

func checkValidity(cfg *config.Config, ca *certs.CA) {
	now := time.Now()
	if ca.Cert.NotAfter.Before(now) || ca.Cert.NotBefore.After(now) {
		logx.Server(true, "Warning! CA Certificate is out of date: %s-%s, now %s.", ca.Cert.NotBefore, ca.Cert.NotAfter, now)
	}
	if ca.Cert.NotAfter.Before(now.AddDate(0, 0, cfg.SslCertValidAfterNow)) || ca.Cert.NotBefore.After(now.AddDate(0, 0, cfg.SslCertValidBeforeNow)) {
		logx.Server(true, "Warning! CA Certificate is too fresh or expires too soon. Check configuration.")
	}
}
