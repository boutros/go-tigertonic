package tigertonic

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"sort"
	"strings"
)

// TrieServeMux is an HTTP request multiplexer that implements http.Handler
// with an API similar to http.ServeMux.  It is expanded to be sensitive to the
// HTTP method and treats URL patterns as patterns rather than simply prefixes.
//
// Components of the URL pattern surrounded by braces (for example: "{foo}")
// match any string and create an entry for the string plus the string
// surrounded by braces in the query parameters (for example: "foo" and
// "{foo}").
type TrieServeMux struct {
	methods map[string]http.Handler
	param   *string
	paths   map[string]*TrieServeMux
	pattern string
}

// NewTrieServeMux makes a new TrieServeMux.
func NewTrieServeMux() *TrieServeMux {
	return &TrieServeMux{
		methods: make(map[string]http.Handler),
		paths:   make(map[string]*TrieServeMux),
	}
}

// Handle registers an http.Handler for the given HTTP method and URL pattern.
func (mux *TrieServeMux) Handle(method, pattern string, handler http.Handler) {
	log.Printf("handling %s %s\n", method, pattern)
	mux.add(method, strings.Split(pattern, "/")[1:], handler, pattern)
}

// HandleFunc registers a handler function for the given HTTP method and URL
// pattern.
func (mux *TrieServeMux) HandleFunc(method, pattern string, handler func(http.ResponseWriter, *http.Request)) {
	mux.Handle(method, pattern, http.HandlerFunc(handler))
}

// HandleNamespace registers an http.Handler for the given URL namespace.
// The matching namespace is stripped from the URL before it is passed to the
// underlying http.Handler.
func (mux *TrieServeMux) HandleNamespace(namespace string, handler http.Handler) {
	log.Printf("handling namespace %s\n", namespace)
	mux.add("", strings.Split(namespace, "/")[1:], handler, namespace)
}

// Handler returns the handler to use for the given HTTP request and mutates
// the querystring to add wildcards extracted from the URL.
//
// Yes, it's bad that this mutates the request.  On the other hand, this is
// a relatively standard interface and is most used in testing where behavior
// like this can be allowed.
func (mux *TrieServeMux) Handler(r *http.Request) (http.Handler, string) {
	params, handler, pattern := mux.find(r, strings.Split(r.URL.Path, "/")[1:])
	r.URL.RawQuery = r.URL.RawQuery + "&" + params.Encode()
	return handler, pattern
}

// ServeHTTP routes an HTTP request to the http.Handler registered for the URL
// pattern which matches the requested path.  It responds 404 if there is no
// matching URL pattern and 405 if the requested HTTP method is not allowed.
func (mux *TrieServeMux) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	handler, _ := mux.Handler(r)
	handler.ServeHTTP(w, r)
}

// add recursively adds a URL pattern, parsing wildcards as it goes, to
// the trie and registers an http.Handler to handle an HTTP method.  An empty
// method indicates a namespace.
func (mux *TrieServeMux) add(method string, paths []string, handler http.Handler, pattern string) {
	if 0 == len(paths) {
		mux.methods[method] = handler
		mux.pattern = pattern
		return
	}
	if strings.HasPrefix(paths[0], "{") && strings.HasSuffix(paths[0], "}") {
		mux.param = &paths[0]
	}
	if _, ok := mux.paths[paths[0]]; !ok {
		mux.paths[paths[0]] = NewTrieServeMux()
	}
	mux.paths[paths[0]].add(method, paths[1:], handler, pattern)
}

// find recursively searches for a URL pattern in the trie, strips
// namespace components from the URL, adds wildcards to the query parameters,
// and returns extra query parameters, a handler, and the pattern that matched.
func (mux *TrieServeMux) find(r *http.Request, paths []string) (url.Values, http.Handler, string) {
	if 0 == len(paths) {
		if handler, ok := mux.methods[r.Method]; ok {
			return nil, handler, mux.pattern
		}
		return nil, methodNotAllowedHandler{mux}, ""
	}
	if _, ok := mux.paths[paths[0]]; ok {
		return mux.paths[paths[0]].find(r, paths[1:])
	}
	if nil != mux.param {
		params, handler, pattern := mux.paths[*mux.param].find(r, paths[1:])
		if nil == params {
			params = make(url.Values)
		}
		params.Set(*mux.param, paths[0])
		params.Set(strings.Trim(*mux.param, "{}"), paths[0])
		return params, handler, pattern
	}
	if handler, ok := mux.methods[""]; ok {
		r.URL.Path = "/" + strings.Join(paths, "/")
		return nil, handler, mux.pattern
	}
	return nil, NotFoundHandler(), ""
}

type methodNotAllowedHandler struct {
	mux *TrieServeMux
}

func (h methodNotAllowedHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	methods := []string{"OPTIONS"}
	if _, ok := h.mux.methods["GET"]; ok {
		methods = append(methods, "HEAD")
	}
	for method, _ := range h.mux.methods {
		methods = append(methods, method)
	}
	sort.Strings(methods)
	w.Header().Set("Allow", strings.Join(methods, ", "))
	if "OPTIONS" == r.Method {
		if method := r.Header.Get(CORSRequestMethod); method != "" {
			w.Header().Set(CORSAllowMethods, strings.Join(methods, ", "))
			if requestOrigin := r.Header.Get(CORSRequestOrigin); requestOrigin != "" {
				allowedOrigin := ""
				if cors, ok := h.mux.methods[method].(*CORSHandler); ok {
					allowedOrigin = cors.getAllowedOrigin(requestOrigin)
				}

				if allowedOrigin == "" {
					allowedOrigin = "null"
				}
				w.Header().Set(CORSAllowOrigin, allowedOrigin)
			}
			if requestHeaders := r.Header.Get(CORSRequestHeaders); requestHeaders != "" {
				allowedHeaders := ""
				if cors, ok := h.mux.methods[method].(*CORSHandler); ok {
					allowedHeaders = cors.getAllowedHeaders()
				}
				w.Header().Set(CORSAllowHeaders, allowedHeaders)
			}

		}
		if acceptJSON(r) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			if err := json.NewEncoder(w).Encode(map[string][]string{
				"allow": methods,
			}); nil != err {
				log.Println(err)
			}
		} else {
			w.Header().Set("Content-Type", "text/plain")
			w.WriteHeader(http.StatusOK)
			fmt.Fprint(w, strings.Join(methods, ", "))
		}
	} else {
		description := fmt.Sprintf(
			"only %s are allowed",
			strings.Join(methods, ", "),
		)
		if acceptJSON(r) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusMethodNotAllowed)
			var e string
			if SnakeCaseHTTPEquivErrors {
				e = "method_not_allowed"
			} else {
				e = "tigertonic.MethodNotAllowed"
			}
			if err := json.NewEncoder(w).Encode(map[string]string{
				"description": description,
				"error":       e,
			}); nil != err {
				log.Println(err)
			}
		} else {
			w.Header().Set("Content-Type", "text/plain")
			w.WriteHeader(http.StatusMethodNotAllowed)
			fmt.Fprint(w, description)
		}
	}
}
