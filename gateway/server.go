package main

import (
	"net/http"
        "base"
	"crypto/tls"
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base64"
	"os"
	"log"
	"strings"
	"io/ioutil"
	"path"
	"html/template"
	"bytes"
	"fmt"
	"net/url"
	"net/http/httputil"
	"net"
	"time"
	"sync"
	"encoding/json"
	"golang.org/x/crypto/acme/autocert"
)


var tlsCertPath = os.Getenv("TLS_CERT_PATH")
var tlsKeyPath = os.Getenv("TLS_KEY_PATH")
var DNSDomain = os.Getenv("DNS_DOMAIN")
var staticAssetsDir = os.Getenv("STATIC_ASSETS_DIR")
var TTYDHostConsole = os.Getenv("TTYD_HOST_CONSOLE_PORT")
var TTYDem100Bios = os.Getenv("TTYD_EM100_BIOS_PORT")
var TTYDem100iLO = os.Getenv("TTYD_EM100_ILO_PORT")
var CTRLIp = os.Getenv("CTRL_IP")
var certStorage = os.Getenv("CERT_STORAGE")
var ExpectediLOIp = os.Getenv("EXPECT_ILO_IP")
var credentialUri = os.Getenv("CREDENTIALS_URI")
var credentialPort = os.Getenv("CREDENTIALS_TCPPORT")

type serverEntry struct {
        servername string
        ip string
	currentOwner string
	queue int
	expiration time.Time
}

type serversList struct {
	servers []serverEntry
	mux sync.Mutex
}

var ciServers serversList

// httpsRedirect redirects http requests to https
func httpsRedirect(w http.ResponseWriter, r *http.Request) {
    http.Redirect(
        w, r,
        "https://"+r.Host+r.URL.String(),
        http.StatusMovedPermanently,
    )
}

func ShiftPath(p string) (head, tail string) {
    p = path.Clean("/" + p)
    i := strings.Index(p[1:], "/") + 1
    if i <= 0 {
        return p[1:], "/"
    }
    return p[1:i], p[i:]
}

func checkAccess(w http.ResponseWriter, r *http.Request) (bool){
	var url = r.URL.Path
	var command string
	entries := strings.Split(strings.TrimSpace(url[1:]), "/") 
	var login string

	// The login is always accessible
	if ( len(entries) > 2 ) {
		command = entries[2]
		login = entries[1]
	} 
	switch command {
		case "getToken":
				if ( r.Method == http.MethodGet || r.Method == http.MethodPost ) {
					return true
				} else {
					return false
				}
		case "validateUser":
				return true
		case "resetPassword":
				return true
		case "generatePasswordLnkRst":
				return true
		case "createUser":
				return true
	}
        if ( r.Header.Get("Authorization") != "" ) {
		var method string
		switch r.Method {
			case http.MethodGet:
				method = "GET"
			case http.MethodPut:
				method = "PUT"
			case http.MethodPost:
				method = "POST"
			case http.MethodDelete:
				method = "DELETE"
		}
                // Is this an AWS request ?
                words := strings.Fields(r.Header.Get("Authorization"))
                if ( words[0] == "JYP" ) {
                        // Let's dump the various content
                        keys := strings.Split(words[1],":")
                        // We must retrieve the secret key used for encryption and calculate the header
                        // if everything is ok (aka our computed value match) we are good

			path := strings.Split( r.URL.Path, "/" )
		        if ( len(path) < 3 ) {
		                http.Error(w, "401 Malformed URI", 401)
		                return false
		        }
		        username := path[2]

			result:=base.HTTPGetRequest("http://"+r.Host+":9100"+"/user/"+username+"/userGetInternalInfo")

			var return_data *base.User
			return_data = new(base.User)
                        json.Unmarshal([]byte(result),return_data)

			// I am getting the Secret Key and the Nickname
                        stringToSign := method + "\n\n"+r.Header.Get("Content-Type")+"\n"+r.Header.Get("myDate")+"\n"+r.URL.Path

			secretKey := return_data.TokenSecret
			nickname := username
			if ( nickname != login ) {
				return false
			}
                        mac := hmac.New(sha1.New, []byte(secretKey))
                        mac.Write([]byte(stringToSign))
                        expectedMAC := mac.Sum(nil)
                        if ( base64.StdEncoding.EncodeToString(expectedMAC) == keys[1] ) {
				return true
                        }
                }
	}
	return false
}

func user(w http.ResponseWriter, r *http.Request) {

	if ( !checkAccess(w, r)  ) {
		w.Write([]byte("Access denied"))
		return
	}

	// parse the url
	url, _ := url.Parse("http://"+credentialUri+credentialPort)

	// create the reverse proxy
	proxy := httputil.NewSingleHostReverseProxy(url)

	// Update the headers to allow for SSL redirection
	r.URL.Host = "http://"+r.Host+":9100"

	r.Header.Set("X-Forwarded-Host", r.Header.Get("Host"))

	// Note that ServeHttp is non blocking and uses a go routine under the hood
	proxy.ServeHTTP(w , r)
}

func home(w http.ResponseWriter, r *http.Request) {

	// The cookie allow us to track the current
	// user on the node
        cookie, _ := r.Cookie("osfci_cookie")

	head, tail := ShiftPath( r.URL.Path)
	if ( head == "ci" ) {
		head,_ = ShiftPath(tail)
	}

	// If the request is different than a getServer
	// We must be sure that the end user still has an active server
	// If that is not the case we deny the request
	// And need to re route the end user to an end of session

	switch ( head ) {
		case "getServer":
			// We need to have a valid cookie and associated Public Key / Private Key otherwise
			// We can't request a server
			if ( cookie.Value != "" ) {
				// To do so I must sent the cookie value to the user API and
				// get a respond. If it is gone we must denied the demand
				type returnValue struct {
                                        Servername string
                                        Waittime string
					Queue string
					RemainingTime string
                                }
                                var myoutput returnValue	
				ciServers.mux.Lock()
				actualTime := time.Now().Add(time.Second*3600*365*10)
				index := 0
				for i, _ := range ciServers.servers { 
					if ( time.Now().After(ciServers.servers[i].expiration) ) {
						// the server is available we can allocate it
						ciServers.servers[i].expiration = time.Now().Add(time.Second*time.Duration(base.MaxServerAge))
						ciServers.servers[i].currentOwner = cookie.Value
						ciServers.mux.Unlock()

						myoutput.Servername = ciServers.servers[i].servername
						myoutput.Waittime = "0"
						myoutput.RemainingTime = fmt.Sprintf("%d",base.MaxServerAge)
						return_data,_ := json.Marshal(myoutput)
						if ( ciServers.servers[i].queue > 0 ) {
							ciServers.servers[i].queue = ciServers.servers[i].queue - 1
						}
						w.Write([]byte(return_data))
						// We probably need to turn it off just to clean it
						return
					}
					if ( actualTime.After(ciServers.servers[i].expiration) ) {
						actualTime = ciServers.servers[i].expiration
						index = i
					}
					
				}
				ciServers.mux.Unlock()
				myoutput.Servername = ""
				remainingTime := actualTime.Sub(time.Now())
				myoutput.Waittime = fmt.Sprintf("%.0f", remainingTime.Seconds())
				myoutput.Queue = fmt.Sprintf("%d",ciServers.servers[index].queue)
				ciServers.servers[index].queue = ciServers.servers[index].queue + 1	
				myoutput.RemainingTime = fmt.Sprintf("%d",0)
				return_data,_ := json.Marshal(myoutput)
				w.Write([]byte(return_data))
			}

		case "console":
			fmt.Printf("Console request\n");
		        url, _ := url.Parse("http://"+CTRLIp+TTYDHostConsole)
		        proxy := httputil.NewSingleHostReverseProxy(url)
		        r.URL.Host = "http://"+CTRLIp+TTYDHostConsole
			filePath :=  strings.Split(tail,"/")
			r.URL.Path = "/"
			if ( len(filePath) > 2 ) {
				r.URL.Path = r.URL.Path + filePath[2]
			}
		        r.Header.Set("X-Forwarded-Host", r.Header.Get("Host"))
			proxy.ServeHTTP(w , r)
		case "smbiosconsole":
                        url, _ := url.Parse("http://"+CTRLIp+TTYDem100Bios)
                        proxy := httputil.NewSingleHostReverseProxy(url)
                        r.URL.Host = "http://"+CTRLIp+TTYDem100Bios
                        filePath :=  strings.Split(tail,"/")
                        r.URL.Path = "/"
                        if ( len(filePath) > 2 ) {
                                r.URL.Path = r.URL.Path + filePath[2]
                        }
                        r.Header.Set("X-Forwarded-Host", r.Header.Get("Host"))
                        proxy.ServeHTTP(w , r)
		case "poweron":
			fmt.Printf("Poweron request\n");
			client := &http.Client{}
                        var req *http.Request
                        req, _ = http.NewRequest("GET","http://"+CTRLIp+"/poweron", nil)
                        _, _  = client.Do(req)
		case "poweroff":
			fmt.Printf("Poweroff request\n");
			client := &http.Client{}
                        var req *http.Request
                        req, _ = http.NewRequest("GET","http://"+CTRLIp+"/poweroff", nil)
                        _, _  = client.Do(req)
		case "iloconsole":
                        url, _ := url.Parse("http://"+CTRLIp+TTYDem100iLO)
                        proxy := httputil.NewSingleHostReverseProxy(url)
                        r.URL.Host = "http://"+CTRLIp+TTYDem100iLO
                        filePath :=  strings.Split(tail,"/")
                        r.URL.Path = "/"
                        if ( len(filePath) > 2 ) {
                                r.URL.Path = r.URL.Path + filePath[2]
                        }
                        r.Header.Set("X-Forwarded-Host", r.Header.Get("Host"))
                        proxy.ServeHTTP(w , r)
		case "startilo":
			// we must forward the request to the relevant test server
			client := &http.Client{}
			var req *http.Request
			req, _ = http.NewRequest("GET","http://"+CTRLIp+"/startilo", nil)
		        _, _  = client.Do(req)

			client = &http.Client{}
                        req, _ = http.NewRequest("GET","http://"+CTRLIp+"/startiloconsole", nil)
                        _, _  = client.Do(req)
		case "startsmbios":
			// we must forward the request to the relevant test server
                        client := &http.Client{}
                        var req *http.Request
                        req, _ = http.NewRequest("GET","http://"+CTRLIp+"/startsmbios", nil)
                        _, _  = client.Do(req)
		case "js":
			b, _ := ioutil.ReadFile(staticAssetsDir+tail) // just pass the file name
                        w.Write(b)
		case "html":
			b, _ := ioutil.ReadFile(staticAssetsDir+tail) // just pass the file name
                        w.Write(b)
		case "css":
			b, _ := ioutil.ReadFile(staticAssetsDir+tail) // just pass the file name
			w.Header().Set("Content-Type", "text/css; charset=utf-8")
                        w.Write(b)
		case "images":
			b, _ := ioutil.ReadFile(staticAssetsDir+tail) // just pass the file name
			w.Header().Set("Content-Type", "image/png")
			w.Write(b)
		case "mp4":
			b, _ := ioutil.ReadFile(staticAssetsDir+tail) // just pass the file name
                        w.Header().Set("Content-Type", "video/mp4")
                        w.Write(b)
		case "ilofirmware":
			// We must forward the request
			fmt.Printf("Forward ilofirmware upload\n");
                        url, _ := url.Parse("http://"+CTRLIp)
                        proxy := httputil.NewSingleHostReverseProxy(url)
                        r.URL.Host = "http://"+CTRLIp
                        r.URL.Path = "/ilofirmware"
                        r.Header.Set("X-Forwarded-Host", r.Header.Get("Host"))
                        proxy.ServeHTTP(w , r)
		case "biosfirmware":
			// We must forward the request
                        fmt.Printf("Forward biosfirmware upload\n");
                        url, _ := url.Parse("http://"+CTRLIp)
                        proxy := httputil.NewSingleHostReverseProxy(url)
                        r.URL.Host = "http://"+CTRLIp
                        r.URL.Path = "/biosfirmware"
                        r.Header.Set("X-Forwarded-Host", r.Header.Get("Host"))
                        proxy.ServeHTTP(w , r)
		case "":
                        b, _ := ioutil.ReadFile(staticAssetsDir+"/html/homepage.html") // just pass the file name
                        // this is a potential template file we need to replace the http field
                        // by the calling r.Host
                        t := template.New("my template")
                        buf := &bytes.Buffer{}
                        t.Parse(string(b))
                        t.Execute(buf, r.Host+"/ci/")
                        fmt.Fprintf(w, buf.String())
		default:
	}
}

func iloweb(w http.ResponseWriter, r *http.Request){
	// Let's print the session ID
//        cookie, err := r.Cookie("osfci_cookie")


	// If the request is for a favicon.ico file we are just returning
	// we do not offer such icon currently ;)
	head, _ := ShiftPath( r.URL.Path)
	if ( head == "favicon.ico" ) {
		return
	}

	// We must know if iLo is started or not ?
	// if not then we have to reroute to the actual homepage
	// We can make a request to the website or
	conn, err := net.DialTimeout("tcp", ExpectediLOIp+":443", 220*time.Millisecond)
	if ( err != nil ) {
		if ( DNSDomain != "" ) {
			http.Redirect(w, r, "https://"+DNSDomain+"/ci", 302)
		}
		return
	} else {
		conn.Close()
	}
	// Must specify the iLo Web address
	url, _ := url.Parse("https://"+ExpectediLOIp+":443")
	proxy := httputil.NewSingleHostReverseProxy(url)
	var InsecureTransport http.RoundTripper = &http.Transport{
		Dial: (&net.Dialer{
		Timeout:   30 * time.Second,
		KeepAlive: 30 * time.Second,
		}).Dial,
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
			TLSHandshakeTimeout: 10 * time.Second,
	}
	// Our OpenBMC has a self signed certificate
	proxy.Transport = InsecureTransport
	// Internal gateway IP address
	// Must reroute on myself and port 443
        url, _ = url.Parse("http://"+r.Header.Get("Host"))
	r.URL.Host = "https://"+url.Hostname()+":443/"
	r.Header.Set("X-Forwarded-Host", r.Header.Get("Host"))
	proxy.ServeHTTP(w , r)

}

func main() {
    print("=============================== \n")
    print("| Starting frontend           |\n")
    print("| Development version -       |\n")
    print("| Private use only            |\n")
    print("=============================== \n")
    print(" Please do not forget to set TLS_CERT_PATH/TLS_KEY_PATH/STATIC_ASSETS_DIR to there relevant path\n")

    mux := http.NewServeMux()

    // Highest priority must be set to the signed request
    mux.HandleFunc("/ci/",home)
    mux.HandleFunc("/user/", user)
    mux.HandleFunc("/",iloweb)

    // We must build our server pool for the moment
    // This is define by the environment variable
    // But this could be done by a registration mechanism later
    var newEntry serverEntry

    newEntry.servername = "dl360"
    newEntry.ip=CTRLIp
    newEntry.currentOwner=""
    // the server is expired
    newEntry.expiration = time.Now()
    newEntry.queue = 0

    ciServers.mux.Lock()
    ciServers.servers = append(ciServers.servers, newEntry)
    ciServers.mux.Unlock()

    if ( DNSDomain != "" ) {
        // if DNS_DOMAIN is set then we run in a production environment
        // we must get the directory where the certificates will be stored
        certManager := autocert.Manager{
                Prompt: autocert.AcceptTOS,
                Cache:  autocert.DirCache(certStorage),
                HostPolicy: autocert.HostWhitelist(DNSDomain),
        }

        server := &http.Server{
                Addr:    ":443",
                Handler: mux,
                ReadTimeout:  600 * time.Second,
                WriteTimeout: 600 * time.Second,
                IdleTimeout:  120 * time.Second,
                TLSConfig: &tls.Config{
                        GetCertificate: certManager.GetCertificate,
                },
        }

        go func() {
        h := certManager.HTTPHandler(nil)
                log.Fatal(http.ListenAndServe(":http", h))
        }()

        server.ListenAndServeTLS("", "")
     } else {
    		go http.ListenAndServe(":80", http.HandlerFunc(httpsRedirect))
	    	// Launch TLS server
	    	log.Fatal(http.ListenAndServeTLS(":443", tlsCertPath, tlsKeyPath, mux))
     }
}
