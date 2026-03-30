package weixin

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"
)

const (
	DefaultLongPollTimeout = 35 * time.Second
	DefaultApiTimeout      = 15 * time.Second
	DefaultConfigTimeout   = 10 * time.Second
)

type ApiOptions struct {
	BaseURL   string
	Token     string
	Timeout   time.Duration
	LongPoll  time.Duration
}

func randomWechatUin() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	uin := uint32(b[0])<<24 | uint32(b[1])<<16 | uint32(b[2])<<8 | uint32(b[3])
	return base64.StdEncoding.EncodeToString([]byte(strconv.FormatUint(uint64(uin), 10)))
}

func (o *ApiOptions) buildHeaders(body []byte) http.Header {
	h := make(http.Header)
	h.Set("Content-Type", "application/json")
	h.Set("AuthorizationType", "ilink_bot_token")
	h.Set("Content-Length", strconv.Itoa(len(body)))
	h.Set("X-WECHAT-UIN", randomWechatUin())
	if o.Token != "" {
		h.Set("Authorization", "Bearer "+o.Token)
	}
	// Note: SKRouteTag is loaded from account config in TS, we might need a way to pass it.
	return h
}

func (o *ApiOptions) fetch(ctx context.Context, endpoint string, reqBody any, timeout time.Duration) ([]byte, error) {
	body, err := json.Marshal(reqBody)
	if err != nil {
		return nil, err
	}

	url := o.BaseURL
	if url[len(url)-1] != '/' {
		url += "/"
	}
	url += endpoint

	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header = o.buildHeaders(body)

	client := &http.Client{Timeout: timeout}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("weixin: api error %d: %s", resp.StatusCode, string(respBody))
	}

	return respBody, nil
}

func (o *ApiOptions) GetUpdates(ctx context.Context, req *GetUpdatesReq) (*GetUpdatesResp, error) {
	timeout := o.LongPoll
	if timeout == 0 {
		timeout = DefaultLongPollTimeout
	}
	// We use slightly longer context timeout than HTTP client timeout to handle network issues gracefully
	respBody, err := o.fetch(ctx, "ilink/bot/getupdates", req, timeout+2*time.Second)
	if err != nil {
		return nil, err
	}
	var resp GetUpdatesResp
	if err := json.Unmarshal(respBody, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

func (o *ApiOptions) SendMessage(ctx context.Context, req *SendMessageReq) error {
	_, err := o.fetch(ctx, "ilink/bot/sendmessage", req, DefaultApiTimeout)
	return err
}

func (o *ApiOptions) GetUploadURL(ctx context.Context, req *GetUploadUrlReq) (*GetUploadUrlResp, error) {
	respBody, err := o.fetch(ctx, "ilink/bot/getuploadurl", req, DefaultApiTimeout)
	if err != nil {
		return nil, err
	}
	var resp GetUploadUrlResp
	if err := json.Unmarshal(respBody, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

func (o *ApiOptions) GetConfig(ctx context.Context, ilinkUserID, contextToken string) (*GetConfigResp, error) {
	req := map[string]string{
		"ilink_user_id": ilinkUserID,
		"context_token": contextToken,
	}
	respBody, err := o.fetch(ctx, "ilink/bot/getconfig", req, DefaultConfigTimeout)
	if err != nil {
		return nil, err
	}
	var resp GetConfigResp
	if err := json.Unmarshal(respBody, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

func (o *ApiOptions) SendTyping(ctx context.Context, req *SendTypingReq) error {
	_, err := o.fetch(ctx, "ilink/bot/sendtyping", req, DefaultConfigTimeout)
	return err
}
