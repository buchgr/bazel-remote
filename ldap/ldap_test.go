package ldap

import (
    "context"
    b64 "encoding/base64"
    "fmt"
    "io"
    "log"
    "net/http"
    "strings"
    "sync"
    "testing"
    "time"

    "github.com/JonasScharpf/godap/godap"
    simplesearch "github.com/JonasScharpf/godap/godap"
    "github.com/abbot/go-http-auth"
    config "github.com/buchgr/bazel-remote/v2/config"
)

func loadYamlConfig(data []byte) *config.Config {
    cfg, err := config.NewConfigFromYaml(data)
    if err != nil {
        log.Fatal(err)
    }
    return cfg
}

func loadFakeLdapConfig() *config.Config {
    yaml := `host: localhost
port: 8080
dir: /opt/cache-dir
max_size: 100
ldap:
  url: ldap://127.0.0.99:10000/
  base_dn: OU=My Users,DC=example,DC=com
  username_attribute: uid
  bind_user: CN=read-only-admin,OU=My Users,DC=example,DC=com
  bind_password: 1234
  cache_time: 3600s
  groups:
   - CN=bazel-users,OU=Groups,OU=My Users,DC=example,DC=com
   - CN=other-users,OU=Groups2,OU=Alien Users,DC=foo,DC=org
`

    return loadYamlConfig([]byte(yaml))
}

var usersPasswords = map[string]string{
    "CN=read-only-admin,OU=My Users,DC=example,DC=com": "1234",
    "user":                                  "password",
    "cn=user,OU=My Users,DC=example,DC=com": "password",
}

func verifyUserPass(username string, password string) bool {
    log.Printf("Looking for username '%s' with password '%s'", username, password)
    wantPass, hasUser := usersPasswords[username]
    if !hasUser {
        log.Printf("No such user '%s'", username)
        return false
    }
    if wantPass == password {
        log.Println("Password and username are valid")
        return true
    }
    log.Printf("Invalid password for username '%s'", username)
    return false
}

func startLdapServer() {
    hs := make([]godap.LDAPRequestHandler, 0)

    // use a LDAPBindFuncHandler to provide a callback function to respond
    // to bind requests
    hs = append(hs, &godap.LDAPBindFuncHandler{
        LDAPBindFunc: func(binddn string, bindpw []byte) bool {
            return verifyUserPass(binddn, string(bindpw))
        },
    })

    // use a LDAPSimpleSearchFuncHandler to reply to search queries
    hs = append(hs, &simplesearch.LDAPSimpleSearchFuncHandler{
        LDAPSimpleSearchFunc: func(req *godap.LDAPSimpleSearchRequest) []*godap.LDAPSimpleSearchResultEntry {
            ret := make([]*godap.LDAPSimpleSearchResultEntry, 0, 1)

            if req.FilterAttr == "uid" {
                userPassword := b64.StdEncoding.EncodeToString([]byte(req.FilterValue))

                ret = append(ret, &simplesearch.LDAPSimpleSearchResultEntry{
                    DN: "cn=" + req.FilterValue + "," + req.BaseDN,
                    Attrs: map[string]interface{}{
                        "cn":            req.FilterValue,
                        "sn":            req.FilterValue,
                        "uid":           req.FilterValue,
                        "userPassword":  userPassword,
                        "homeDirectory": "/home/" + req.FilterValue,
                        "objectClass": []string{
                            "top",
                            "posixAccount",
                            "inetOrgPerson",
                        },
                    },
                    Skip: false,
                })
            } else if req.FilterAttr == "searchFingerprint" {
                // a non-simple search request has been received and should be
                // processed. For simplicity, as this is just a fake LDAP
                // server simple but really bad assumptions are done onwards.
                // If the first query element is "pass" a response is sent,
                // otherwise not. By this a user can be available/found or not
                filterValues := strings.Split(req.FilterValue, ";")
                passOrFail := filterValues[0]
                user := filterValues[1]
                userPassword := b64.StdEncoding.EncodeToString([]byte(user))
                // TODO add user with this password to the mapping

                if passOrFail == "pass" {
                    log.Println("Simulate 'query match'")
                    ret = append(ret, &simplesearch.LDAPSimpleSearchResultEntry{
                        DN: "cn=" + user + "," + req.BaseDN,
                        Attrs: map[string]interface{}{
                            "cn":            user,
                            "sn":            user,
                            "uid":           user,
                            "userPassword":  userPassword,
                            "homeDirectory": "/home/" + user,
                            "objectClass": []string{
                                "top",
                                "posixAccount",
                                "inetOrgPerson",
                            },
                        },
                        Skip: false,
                    })
                } else {
                    log.Println("Simulate 'no query match'")
                    // "skip" this one in the LDAP processing step to mock an
                    // empty response
                    ret = append(ret, &simplesearch.LDAPSimpleSearchResultEntry{
                        DN: "cn=" + user + "," + req.BaseDN,
                        Attrs: map[string]interface{}{
                            "cn": user,
                        },
                        Skip: true,
                    })
                }
            }

            return ret
        },
    })

    s := &godap.LDAPServer{
        Handlers: hs,
    }

    // start the LDAP server and wait for a short time to bring it up,
    // connection would be refused otherwise
    go s.ListenAndServe("127.0.0.99:10000")
    time.Sleep(50 * time.Millisecond)
}

func startHttpServer(ldapAuth auth.AuthenticatorInterface, addr string, timeout time.Duration) (*http.Server, *sync.WaitGroup) {
    mux := http.NewServeMux()

    mux.HandleFunc("/", func(w http.ResponseWriter, req *http.Request) {
        if req.URL.Path != "/" {
            http.NotFound(w, req)
            return
        }
        fmt.Fprintf(w, "Unrestricted")
    })
    mux.HandleFunc("/secret", ldapAuthWrapper(
        func(w http.ResponseWriter, req *http.Request) {
            fmt.Fprintf(w, "Logged in")
        },
        ldapAuth,
    ))

    srv := &http.Server{
        Addr:    addr,
        Handler: mux,
    }

    log.Printf("Starting HTTP server on %s for %s", addr, timeout)

    httpServerExitDone := &sync.WaitGroup{}
    httpServerExitDone.Add(1)
    go func() {
        defer httpServerExitDone.Done()

        // always returns error. ErrServerClosed on graceful close
        if err := srv.ListenAndServe(); err != http.ErrServerClosed {
            log.Fatal("HTTP server error:", err)
        }
    }()

    // start the HTTP server and wait for a short time to bring it up,
    // connection would be refused otherwise
    time.Sleep(50 * time.Millisecond)

    return srv, httpServerExitDone
}

func TestNewConnection(t *testing.T) {
    cfg := loadFakeLdapConfig()
    ldapAuthenticator, ldap_err := New(cfg.LDAP)

    if ldapAuthenticator != nil {
        t.Fatal("No connection should be established to", cfg.LDAP.URL)
    }
    if ldap_err == nil {
        t.Fatal("An error should raise while connecting to", cfg.LDAP.URL)
    }

    startLdapServer()

    ldapAuthenticator, ldap_err = New(cfg.LDAP)

    if ldapAuthenticator == nil {
        t.Fatal("Connection should be established to", cfg.LDAP.URL)
    }
    if ldap_err != nil {
        t.Fatal("No error should raise while connecting to", cfg.LDAP.URL)
    }

    // set an invalid bind password
    cfg.LDAP.BindPassword = "asdf"
    ldapAuthenticator, ldap_err = New(cfg.LDAP)

    if ldapAuthenticator != nil {
        t.Fatal("No connection should be established with", cfg.LDAP.BindPassword)
    }
    if ldap_err == nil {
        t.Fatal("An error should raise while connecting with", cfg.LDAP.BindPassword)
    }
}

func TestAuth(t *testing.T) {
    cfg := loadFakeLdapConfig()
    var ldapAuthenticator auth.AuthenticatorInterface
    var ldap_err error
    var httpServerAddr string = "127.0.0.99:4000"
    var httpServerTimeout time.Duration = 5 * time.Second
    // allow the onwards used user to successfully login
    cfg.LDAP.UsernameAttribute = "pass"

    startLdapServer()

    ldapAuthenticator, ldap_err = New(cfg.LDAP)

    if ldapAuthenticator == nil {
        t.Fatal("Connection should be established to", cfg.LDAP.URL)
    }
    if ldap_err != nil {
        t.Fatal("No error should raise while connecting to", cfg.LDAP.URL)
    }

    srv, httpServerExitDone := startHttpServer(ldapAuthenticator, httpServerAddr, httpServerTimeout)

    pageContent := crawlHttpPage("http://" + httpServerAddr)
    if pageContent != "Unrestricted" {
        t.Fatal("No content received from root page, expected 'Unrestricted'")
    }

    securePageContent := crawlHttpPage("http://"+httpServerAddr+"/secret", "user", usersPasswords["user"])
    if securePageContent != "Logged in" {
        t.Fatal("No content received from '/secret' page, expected 'Logged in'")
    }

    // uncomment this sleep for manual testing and HTTP/LDAP interaction
    // time.Sleep(60 * time.Second)

    if err := srv.Shutdown(context.Background()); err != nil {
        log.Fatal("HTTP shutdown error:", err)
    }

    // wait for started goroutine to stop
    httpServerExitDone.Wait()
    log.Println("HTTP server shutdown completed")
}

func ldapAuthWrapper(handler http.HandlerFunc, authenticator auth.AuthenticatorInterface) http.HandlerFunc {
    return auth.JustCheck(authenticator, handler)
}

func crawlHttpPage(params ...string) string {
    client := http.Client{Timeout: 1 * time.Second}

    req, err := http.NewRequest(http.MethodGet, params[0], http.NoBody)
    if err != nil {
        log.Fatal(err)
    }

    if len(params) == 3 {
        req.SetBasicAuth(params[1], params[2])
    }

    res, err := client.Do(req)
    if err != nil {
        log.Fatal(err)
    }

    defer res.Body.Close()

    resBody, err := io.ReadAll(res.Body)
    if err != nil {
        log.Fatal(err)
    }

    return string(resBody)
}
