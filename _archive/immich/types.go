package immich

// JSON DTOs mirroring (a subset of) the Immich API. Field names and shapes are
// chosen to match what the Immich mobile/web clients expect. This is a
// best-effort mapping over a Google Photos library; some fields are stubbed
// with sensible constants because the underlying data does not exist.

type pingResponse struct {
	Res string `json:"res"`
}

type versionResponse struct {
	Major int `json:"major"`
	Minor int `json:"minor"`
	Patch int `json:"patch"`
}

type loginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

type loginResponse struct {
	AccessToken          string `json:"accessToken"`
	UserID               string `json:"userId"`
	UserEmail            string `json:"userEmail"`
	Name                 string `json:"name"`
	IsAdmin              bool   `json:"isAdmin"`
	ProfileImagePath     string `json:"profileImagePath"`
	ShouldChangePassword bool   `json:"shouldChangePassword"`
}

type validateTokenResponse struct {
	AuthStatus bool `json:"authStatus"`
}

type userResponse struct {
	ID                   string  `json:"id"`
	Email                string  `json:"email"`
	Name                 string  `json:"name"`
	ProfileImagePath     string  `json:"profileImagePath"`
	ProfileChangedAt     string  `json:"profileChangedAt"`
	IsAdmin              bool    `json:"isAdmin"`
	ShouldChangePassword bool    `json:"shouldChangePassword"`
	CreatedAt            string  `json:"createdAt"`
	UpdatedAt            string  `json:"updatedAt"`
	DeletedAt            *string `json:"deletedAt"`
	OAuthID              string  `json:"oauthId"`
	QuotaSizeInBytes     *int64  `json:"quotaSizeInBytes"`
	QuotaUsageInBytes    int64   `json:"quotaUsageInBytes"`
	Status               string  `json:"status"`
	StorageLabel         *string `json:"storageLabel"`
	ExternalPath         *string `json:"externalPath"`
	MemoriesEnabled      bool    `json:"memoriesEnabled"`
	AvatarColor          string  `json:"avatarColor"`
}

type serverConfigResponse struct {
	LoginPageMessage string `json:"loginPageMessage"`
	TrashDays        int    `json:"trashDays"`
	UserDeleteDelay  int    `json:"userDeleteDelay"`
	OAuthButtonText  string `json:"oauthButtonText"`
	IsInitialized    bool   `json:"isInitialized"`
	IsOnboarded      bool   `json:"isOnboarded"`
	ExternalDomain   string `json:"externalDomain"`
	MapDarkStyleURL  string `json:"mapDarkStyleUrl"`
	MapLightStyleURL string `json:"mapLightStyleUrl"`
}

type serverFeaturesResponse struct {
	SmartSearch       bool `json:"smartSearch"`
	FacialRecognition bool `json:"facialRecognition"`
	Map               bool `json:"map"`
	ReverseGeocoding  bool `json:"reverseGeocoding"`
	ImportFaces       bool `json:"importFaces"`
	Sidecar           bool `json:"sidecar"`
	Search            bool `json:"search"`
	Trash             bool `json:"trash"`
	OAuth             bool `json:"oauth"`
	OAuthAutoLaunch   bool `json:"oauthAutoLaunch"`
	PasswordLogin     bool `json:"passwordLogin"`
	ConfigFile        bool `json:"configFile"`
	Email             bool `json:"email"`
}

type serverAboutResponse struct {
	Version    string `json:"version"`
	VersionURL string `json:"versionUrl"`
	Licensed   bool   `json:"licensed"`
	Build      string `json:"build"`
	Repository string `json:"repository"`
	SourceURL  string `json:"sourceUrl"`
}

type serverStorageResponse struct {
	DiskAvailable       string  `json:"diskAvailable"`
	DiskSize            string  `json:"diskSize"`
	DiskUse             string  `json:"diskUse"`
	DiskAvailableRaw    int64   `json:"diskAvailableRaw"`
	DiskSizeRaw         int64   `json:"diskSizeRaw"`
	DiskUseRaw          int64   `json:"diskUseRaw"`
	DiskUsagePercentage float64 `json:"diskUsagePercentage"`
}

// assetResponse is a subset of Immich AssetResponseDto.
type assetResponse struct {
	ID               string  `json:"id"`
	DeviceAssetID    string  `json:"deviceAssetId"`
	OwnerID          string  `json:"ownerId"`
	DeviceID         string  `json:"deviceId"`
	LibraryID        *string `json:"libraryId"`
	Type             string  `json:"type"` // IMAGE | VIDEO
	OriginalPath     string  `json:"originalPath"`
	OriginalFileName string  `json:"originalFileName"`
	OriginalMimeType string  `json:"originalMimeType,omitempty"`
	Resized          bool    `json:"resized"`
	Thumbhash        *string `json:"thumbhash"`
	FileCreatedAt    string  `json:"fileCreatedAt"`
	FileModifiedAt   string  `json:"fileModifiedAt"`
	LocalDateTime    string  `json:"localDateTime"`
	UpdatedAt        string  `json:"updatedAt"`
	IsFavorite       bool    `json:"isFavorite"`
	IsArchived       bool    `json:"isArchived"`
	IsTrashed        bool    `json:"isTrashed"`
	IsOffline        bool    `json:"isOffline"`
	Duration         string  `json:"duration"`
	Checksum         string  `json:"checksum"`
	HasMetadata      bool    `json:"hasMetadata"`
	LivePhotoVideoID *string `json:"livePhotoVideoId"`
	People           []any   `json:"people"`
}

type timeBucketResponse struct {
	TimeBucket string `json:"timeBucket"`
	Count      int    `json:"count"`
}

// uploadResponse is the Immich AssetMediaResponseDto.
type uploadResponse struct {
	ID     string `json:"id"`
	Status string `json:"status"` // created | replaced | duplicate
}

type errorResponse struct {
	Message    string `json:"message"`
	StatusCode int    `json:"statusCode"`
	Error      string `json:"error"`
}
