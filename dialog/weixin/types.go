package weixin

// BaseInfo represents request metadata attached to every CGI request.
type BaseInfo struct {
	ChannelVersion string `json:"channel_version,omitempty"`
}

// UploadMediaType constants
const (
	UploadMediaTypeImage = 1
	UploadMediaTypeVideo = 2
	UploadMediaTypeFile  = 3
	UploadMediaTypeVoice = 4
)

// GetUploadUrlReq represents a request for a CDN upload URL.
type GetUploadUrlReq struct {
	FileKey         string    `json:"filekey,omitempty"`
	MediaType       int       `json:"media_type,omitempty"`
	ToUserID        string    `json:"to_user_id,omitempty"`
	RawSize         int64     `json:"rawsize,omitempty"`
	RawFileMD5      string    `json:"rawfilemd5,omitempty"`
	FileSize        int64     `json:"filesize,omitempty"`
	ThumbRawSize    int64     `json:"thumb_rawsize,omitempty"`
	ThumbRawFileMD5 string    `json:"thumb_rawfilemd5,omitempty"`
	ThumbFileSize   int64     `json:"thumb_filesize,omitempty"`
	NoNeedThumb     bool      `json:"no_need_thumb,omitempty"`
	AESKey          string    `json:"aeskey,omitempty"`
	BaseInfo        *BaseInfo `json:"base_info,omitempty"`
}

// GetUploadUrlResp represents the response from GetUploadUrl.
type GetUploadUrlResp struct {
	UploadParam      string `json:"upload_param,omitempty"`
	ThumbUploadParam string `json:"thumb_upload_param,omitempty"`
	UploadFullURL    string `json:"upload_full_url,omitempty"` // Direct server-provided URL
}

// Message types
const (
	MessageTypeNone = 0
	MessageTypeUser = 1
	MessageTypeBot  = 2
)

// Message item types
const (
	MessageItemTypeNone  = 0
	MessageItemTypeText  = 1
	MessageItemTypeImage = 2
	MessageItemTypeVoice = 3
	MessageItemTypeFile  = 4
	MessageItemTypeVideo = 5
)

// Message states
const (
	MessageStateNew        = 0
	MessageStateGenerating = 1
	MessageStateFinish     = 2
)

// TextItem represents a text message item.
type TextItem struct {
	Text string `json:"text,omitempty"`
}

// CDNMedia represents a reference to an encrypted file on CDN.
type CDNMedia struct {
	EncryptQueryParam string `json:"encrypt_query_param,omitempty"`
	AESKey            string `json:"aes_key,omitempty"` // Base64 encoded
	EncryptType       int    `json:"encrypt_type,omitempty"`
	FullURL           string `json:"full_url,omitempty"` // Direct server-provided download URL
}

// ImageItem represents an image message item.
type ImageItem struct {
	Media       *CDNMedia `json:"media,omitempty"`
	ThumbMedia  *CDNMedia `json:"thumb_media,omitempty"`
	AESKey      string    `json:"aeskey,omitempty"` // Hex string (preferred over media.aes_key)
	URL         string    `json:"url,omitempty"`
	MidSize     int64     `json:"mid_size,omitempty"`
	ThumbSize   int64     `json:"thumb_size,omitempty"`
	ThumbHeight int       `json:"thumb_height,omitempty"`
	ThumbWidth  int       `json:"thumb_width,omitempty"`
	HDSize      int64     `json:"hd_size,omitempty"`
}

// VoiceItem represents a voice message item.
type VoiceItem struct {
	Media         *CDNMedia `json:"media,omitempty"`
	EncodeType    int       `json:"encode_type,omitempty"`
	BitsPerSample int       `json:"bits_per_sample,omitempty"`
	SampleRate    int       `json:"sample_rate,omitempty"`
	PlayTime      int       `json:"playtime,omitempty"`
	Text          string    `json:"text,omitempty"`
}

// FileItem represents a file message item.
type FileItem struct {
	Media    *CDNMedia `json:"media,omitempty"`
	FileName string    `json:"file_name,omitempty"`
	MD5      string    `json:"md5,omitempty"`
	Len      string    `json:"len,omitempty"`
}

// VideoItem represents a video message item.
type VideoItem struct {
	Media       *CDNMedia `json:"media,omitempty"`
	VideoSize   int64     `json:"video_size,omitempty"`
	PlayLength  int       `json:"play_length,omitempty"`
	VideoMD5    string    `json:"video_md5,omitempty"`
	ThumbMedia  *CDNMedia `json:"thumb_media,omitempty"`
	ThumbSize   int64     `json:"thumb_size,omitempty"`
	ThumbHeight int       `json:"thumb_height,omitempty"`
	ThumbWidth  int       `json:"thumb_width,omitempty"`
}

// RefMessage represents a quoted message.
type RefMessage struct {
	MessageItem *MessageItem `json:"message_item,omitempty"`
	Title       string       `json:"title,omitempty"`
}

// MessageItem represents a single piece of content in a message.
type MessageItem struct {
	Type         int         `json:"type,omitempty"`
	CreateTimeMs int64       `json:"create_time_ms,omitempty"`
	UpdateTimeMs int64       `json:"update_time_ms,omitempty"`
	IsCompleted  bool        `json:"is_completed,omitempty"`
	MsgID        string      `json:"msg_id,omitempty"`
	RefMsg       *RefMessage `json:"ref_msg,omitempty"`
	TextItem     *TextItem   `json:"text_item,omitempty"`
	ImageItem    *ImageItem  `json:"image_item,omitempty"`
	VoiceItem    *VoiceItem  `json:"voice_item,omitempty"`
	FileItem     *FileItem   `json:"file_item,omitempty"`
	VideoItem    *VideoItem  `json:"video_item,omitempty"`
}

// WeixinMessage represents a unified WeChat message.
type WeixinMessage struct {
	Seq          int64          `json:"seq,omitempty"`
	MessageID    int64          `json:"message_id,omitempty"`
	FromUserID   string         `json:"from_user_id,omitempty"`
	ToUserID     string         `json:"to_user_id,omitempty"`
	ClientID     string         `json:"client_id,omitempty"`
	CreateTimeMs int64          `json:"create_time_ms,omitempty"`
	UpdateTimeMs int64          `json:"update_time_ms,omitempty"`
	DeleteTimeMs int64          `json:"delete_time_ms,omitempty"`
	SessionID    string         `json:"session_id,omitempty"`
	GroupID      string         `json:"group_id,omitempty"`
	MessageType  int            `json:"message_type,omitempty"`
	MessageState int            `json:"message_state,omitempty"`
	ItemList     []*MessageItem `json:"item_list,omitempty"`
	ContextToken string         `json:"context_token,omitempty"`
}

// GetUpdatesReq represents the request for message updates.
type GetUpdatesReq struct {
	GetUpdatesBuf string    `json:"get_updates_buf,omitempty"`
	BaseInfo      *BaseInfo `json:"base_info,omitempty"`
}

// GetUpdatesResp represents the response from GetUpdates.
type GetUpdatesResp struct {
	Ret                  int              `json:"ret"`
	ErrCode              int              `json:"errcode,omitempty"`
	ErrMsg               string           `json:"errmsg,omitempty"`
	Msgs                 []*WeixinMessage `json:"msgs,omitempty"`
	GetUpdatesBuf        string           `json:"get_updates_buf,omitempty"`
	LongPollingTimeoutMs int              `json:"longpolling_timeout_ms,omitempty"`
}

// SendMessageReq represents the request to send a message.
type SendMessageReq struct {
	Msg      *WeixinMessage `json:"msg,omitempty"`
	BaseInfo *BaseInfo      `json:"base_info,omitempty"`
}

// TypingStatus constants
const (
	TypingStatusTyping = 1
	TypingStatusCancel = 2
)

// SendTypingReq represents a request to send a typing indicator.
type SendTypingReq struct {
	ILinkUserID  string    `json:"ilink_user_id,omitempty"`
	TypingTicket string    `json:"typing_ticket,omitempty"`
	Status       int       `json:"status,omitempty"` // 1=typing, 2=cancel
	BaseInfo     *BaseInfo `json:"base_info,omitempty"`
}

// GetConfigResp represents the response for bot configuration.
type GetConfigResp struct {
	Ret          int    `json:"ret"`
	ErrMsg       string `json:"errmsg,omitempty"`
	TypingTicket string `json:"typing_ticket,omitempty"`
}
