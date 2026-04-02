package weixin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

const (
	DefaultIlinkBotType = "3"
	QrLongPollTimeout   = 35 * time.Second
	FixedBaseURL        = "https://ilinkai.weixin.qq.com"
)

type QRCodeResponse struct {
	QRCode           string `json:"qrcode"`
	QRCodeImgContent string `json:"qrcode_img_content"`
}

type StatusResponse struct {
	Status       string `json:"status"` // wait, scaned, confirmed, expired, scaned_but_redirect
	BotToken     string `json:"bot_token"`
	IlinkBotID   string `json:"ilink_bot_id"`
	BaseURL      string `json:"baseurl"`
	ILinkUserID  string `json:"ilink_user_id"`
	RedirectHost string `json:"redirect_host"` // For scaned_but_redirect status
}

func (o *ApiOptions) FetchQRCode(ctx context.Context, botType string) (*QRCodeResponse, error) {
	u := o.ensureTrailingSlash(FixedBaseURL)
	endpoint := "ilink/bot/get_bot_qrcode?bot_type=" + url.QueryEscape(botType)
	u += endpoint

	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return nil, err
	}
	// Add iLink compatibility headers
	for k, v := range o.buildCommonHeaders() {
		req.Header.Set(k, v)
	}

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("weixin: qr fetch error %d", resp.StatusCode)
	}

	var qrResp QRCodeResponse
	if err := json.NewDecoder(resp.Body).Decode(&qrResp); err != nil {
		return nil, err
	}
	return &qrResp, nil
}

func (o *ApiOptions) PollQRStatus(ctx context.Context, qrcode string) (*StatusResponse, error) {
	u := o.ensureTrailingSlash(FixedBaseURL)
	endpoint := "ilink/bot/get_qrcode_status?qrcode=" + url.QueryEscape(qrcode)
	u += endpoint

	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return nil, err
	}
	// Add iLink compatibility headers
	for k, v := range o.buildCommonHeaders() {
		req.Header.Set(k, v)
	}

	client := &http.Client{Timeout: QrLongPollTimeout + 2*time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("weixin: qr status poll error %d", resp.StatusCode)
	}

	var statusResp StatusResponse
	if err := json.NewDecoder(resp.Body).Decode(&statusResp); err != nil {
		return nil, err
	}
	return &statusResp, nil
}
