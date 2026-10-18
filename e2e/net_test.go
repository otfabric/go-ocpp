// SPDX-License-Identifier: MIT

package e2e

import (
	"net"
	"net/http"
)

func netListen() (net.Listener, error) { return net.Listen("tcp", "127.0.0.1:0") }

func httpGet(url string, header map[string]string) (int, error) {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return 0, err
	}
	for k, v := range header {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, err
	}
	_ = resp.Body.Close()
	return resp.StatusCode, nil
}

var httpClient = http.Client{}
