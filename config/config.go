package config

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/emvi/logbuch"
	"github.com/gorilla/securecookie"
	"github.com/jinzhu/configor"
	"github.com/muety/wakapi/data"
	"github.com/muety/wakapi/utils"
	"github.com/robfig/cron/v3"
	uuid "github.com/satori/go.uuid"
)

const (
	DefaultConfigPath = "config.yml"

	SQLDialectMysql    = "mysql"
	SQLDialectPostgres = "postgres"
	SQLDialectSqlite   = "sqlite3"

	KeyLatestTotalTime              = "latest_total_time"
	KeyLatestTotalUsers             = "latest_total_users"
	KeyLastImport                   = "last_import"            // import attempt
	KeyLastImportSuccess            = "last_successful_import" // last actual successful import
	KeyFirstHeartbeat               = "first_heartbeat"
	KeySubscriptionNotificationSent = "sub_reminder"
	KeyNewsbox                      = "newsbox"

	SessionKeyDefault = "default"

	SimpleDateFormat     = "2006-01-02"
	SimpleDateTimeFormat = "2006-01-02 15:04:05"

	ErrUnauthorized        = "401 unauthorized"
	ErrBadRequest          = "400 bad request"
	ErrInternalServerError = "500 internal server error"
)

const (
	WakatimeApiUrl               = "https://api.wakatime.com/api/v1"
	WakatimeApiUserUrl           = "/users/current"
	WakatimeApiAllTimeUrl        = "/users/current/all_time_since_today"
	WakatimeApiHeartbeatsUrl     = "/users/current/heartbeats"
	WakatimeApiHeartbeatsBulkUrl = "/users/current/heartbeats.bulk"
	WakatimeApiUserAgentsUrl     = "/users/current/user_agents"
	WakatimeApiMachineNamesUrl   = "/users/current/machine_names"
	WakatimeApiDataDumpUrl       = "/users/current/data_dumps"
)

const (
	MailProviderSmtp      = "smtp"
	MailProviderMailWhale = "mailwhale"
)

var emailProviders = []string{
	MailProviderSmtp,
	MailProviderMailWhale,
}

var cfg *Config
var env string

type appConfig struct {
	AggregationTime           string                       `yaml:"aggregation_time" default:"0 15 2 * * *" env:"WAKAPI_AGGREGATION_TIME"`
	LeaderboardGenerationTime string                       `yaml:"leaderboard_generation_time" default:"0 0 6 * * *,0 0 18 * * *" env:"WAKAPI_LEADERBOARD_GENERATION_TIME"`
	ReportTimeWeekly          string                       `yaml:"report_time_weekly" default:"0 0 18 * * 5" env:"WAKAPI_REPORT_TIME_WEEKLY"`
	DataCleanupTime           string                       `yaml:"data_cleanup_time" default:"0 0 6 * * 0" env:"WAKAPI_DATA_CLEANUP_TIME"`
	ImportEnabled             bool                         `yaml:"import_enabled" default:"true" env:"WAKAPI_IMPORT_ENABLED"`
	ImportBackoffMin          int                          `yaml:"import_backoff_min" default:"5" env:"WAKAPI_IMPORT_BACKOFF_MIN"`
	ImportMaxRate             int                          `yaml:"import_max_rate" default:"24" env:"WAKAPI_IMPORT_MAX_RATE"` // at max one successful import every x hours
	ImportBatchSize           int                          `yaml:"import_batch_size" default:"50" env:"WAKAPI_IMPORT_BATCH_SIZE"`
	InactiveDays              int                          `yaml:"inactive_days" default:"7" env:"WAKAPI_INACTIVE_DAYS"`
	HeartbeatMaxAge           string                       `yaml:"heartbeat_max_age" default:"4320h" env:"WAKAPI_HEARTBEAT_MAX_AGE"`
	CountCacheTTLMin          int                          `yaml:"count_cache_ttl_min" default:"30" env:"WAKAPI_COUNT_CACHE_TTL_MIN"`
	DataRetentionMonths       int                          `yaml:"data_retention_months" default:"-1" env:"WAKAPI_DATA_RETENTION_MONTHS"`
	DataCleanupDryRun         bool                         `yaml:"data_cleanup_dry_run" default:"false" env:"WAKAPI_DATA_CLEANUP_DRY_RUN"` // for debugging only
	AvatarURLTemplate         string                       `yaml:"avatar_url_template" default:"api/avatar/{username_hash}.svg" env:"WAKAPI_AVATAR_URL_TEMPLATE"`
	SupportContact            string                       `yaml:"support_contact" default:"hostmaster@wakapi.dev" env:"WAKAPI_SUPPORT_CONTACT"`
	CustomLanguages           map[string]string            `yaml:"custom_languages"`
	Colors                    map[string]map[string]string `yaml:"-"`
}

type securityConfig struct {
	AllowSignup      bool `yaml:"allow_signup" default:"true" env:"WAKAPI_ALLOW_SIGNUP"`
	ExposeMetrics    bool `yaml:"expose_metrics" default:"false" env:"WAKAPI_EXPOSE_METRICS"`
	EnableProxy      bool `yaml:"enable_proxy" default:"false" env:"WAKAPI_ENABLE_PROXY"` // only intended for production instance at wakapi.dev
	DisableFrontpage bool `yaml:"disable_frontpage" default:"false" env:"WAKAPI_DISABLE_FRONTPAGE"`
	// this is actually a pepper (https://en.wikipedia.org/wiki/Pepper_(cryptography))
	PasswordSalt              string                     `yaml:"password_salt" default:"" env:"WAKAPI_PASSWORD_SALT"`
	InsecureCookies           bool                       `yaml:"insecure_cookies" default:"false" env:"WAKAPI_INSECURE_COOKIES"`
	CookieMaxAgeSec           int                        `yaml:"cookie_max_age" default:"172800" env:"WAKAPI_COOKIE_MAX_AGE"`
	TrustedHeaderAuth         bool                       `yaml:"trusted_header_auth" default:"false" env:"WAKAPI_TRUSTED_HEADER_AUTH"`
	TrustedHeaderAuthKey      string                     `yaml:"trusted_header_auth_key" default:"Remote-User" env:"WAKAPI_TRUSTED_HEADER_AUTH_KEY"`
	TrustReverseProxyIps      string                     `yaml:"trust_reverse_proxy_ips" default:"" env:"WAKAPI_TRUST_REVERSE_PROXY_IPS"` // comma-separated list of trusted reverse proxy ips
	SecureCookie              *securecookie.SecureCookie `yaml:"-"`
	SessionKey                []byte                     `yaml:"-"`
	trustReverseProxyIpParsed []net.IP
}

type dbConfig struct {
	Host                    string `env:"WAKAPI_DB_HOST"`
	Socket                  string `env:"WAKAPI_DB_SOCKET"`
	Port                    uint   `env:"WAKAPI_DB_PORT"`
	User                    string `env:"WAKAPI_DB_USER"`
	Password                string `env:"WAKAPI_DB_PASSWORD"`
	Name                    string `default:"wakapi_db.db" env:"WAKAPI_DB_NAME"`
	Dialect                 string `yaml:"-"`
	Charset                 string `default:"utf8mb4" env:"WAKAPI_DB_CHARSET"`
	Type                    string `yaml:"dialect" default:"sqlite3" env:"WAKAPI_DB_TYPE"`
	DSN                     string `yaml:"DSN" default:"" env:"WAKAPI_DB_DSN"`
	MaxConn                 uint   `yaml:"max_conn" default:"2" env:"WAKAPI_DB_MAX_CONNECTIONS"`
	Ssl                     bool   `default:"false" env:"WAKAPI_DB_SSL"`
	AutoMigrateFailSilently bool   `yaml:"automigrate_fail_silently" default:"false" env:"WAKAPI_DB_AUTOMIGRATE_FAIL_SILENTLY"`
}

type serverConfig struct {
	Port             int    `default:"3000" env:"WAKAPI_PORT"`
	ListenIpV4       string `yaml:"listen_ipv4" default:"127.0.0.1" env:"WAKAPI_LISTEN_IPV4"`
	ListenIpV6       string `yaml:"listen_ipv6" default:"::1" env:"WAKAPI_LISTEN_IPV6"`
	ListenSocket     string `yaml:"listen_socket" default:"" env:"WAKAPI_LISTEN_SOCKET"`
	ListenSocketMode uint32 `yaml:"listen_socket_mode" default:"0666" env:"WAKAPI_LISTEN_SOCKET_MODE"`
	TimeoutSec       int    `yaml:"timeout_sec" default:"30" env:"WAKAPI_TIMEOUT_SEC"`
	BasePath         string `yaml:"base_path" default:"/" env:"WAKAPI_BASE_PATH"`
	PublicUrl        string `yaml:"public_url" default:"http://localhost:3000" env:"WAKAPI_PUBLIC_URL"`
	TlsCertPath      string `yaml:"tls_cert_path" default:"" env:"WAKAPI_TLS_CERT_PATH"`
	TlsKeyPath       string `yaml:"tls_key_path" default:"" env:"WAKAPI_TLS_KEY_PATH"`
}

type subscriptionsConfig struct {
	Enabled              bool   `yaml:"enabled" default:"false" env:"WAKAPI_SUBSCRIPTIONS_ENABLED"`
	ExpiryNotifications  bool   `yaml:"expiry_notifications" default:"true" env:"WAKAPI_SUBSCRIPTIONS_EXPIRY_NOTIFICATIONS"`
	StripeApiKey         string `yaml:"stripe_api_key" env:"WAKAPI_SUBSCRIPTIONS_STRIPE_API_KEY"`
	StripeSecretKey      string `yaml:"stripe_secret_key" env:"WAKAPI_SUBSCRIPTIONS_STRIPE_SECRET_KEY"`
	StripeEndpointSecret string `yaml:"stripe_endpoint_secret" env:"WAKAPI_SUBSCRIPTIONS_STRIPE_ENDPOINT_SECRET"`
	StandardPriceId      string `yaml:"standard_price_id" env:"WAKAPI_SUBSCRIPTIONS_STANDARD_PRICE_ID"`
	StandardPrice        string `yaml:"-"`
}

type sentryConfig struct {
	Dsn                  string  `env:"WAKAPI_SENTRY_DSN"`
	EnableTracing        bool    `yaml:"enable_tracing" env:"WAKAPI_SENTRY_TRACING"`
	SampleRate           float32 `yaml:"sample_rate" default:"0.75" env:"WAKAPI_SENTRY_SAMPLE_RATE"`
	SampleRateHeartbeats float32 `yaml:"sample_rate_heartbeats" default:"0.1" env:"WAKAPI_SENTRY_SAMPLE_RATE_HEARTBEATS"`
}

type mailConfig struct {
	Enabled   bool                `env:"WAKAPI_MAIL_ENABLED" default:"true"`
	Provider  string              `env:"WAKAPI_MAIL_PROVIDER" default:"smtp"`
	MailWhale MailwhaleMailConfig `yaml:"mailwhale"`
	Smtp      SMTPMailConfig      `yaml:"smtp"`
	Sender    string              `env:"WAKAPI_MAIL_SENDER" yaml:"sender"`
}

type MailwhaleMailConfig struct {
	Url          string `env:"WAKAPI_MAIL_MAILWHALE_URL"`
	ClientId     string `yaml:"client_id" env:"WAKAPI_MAIL_MAILWHALE_CLIENT_ID"`
	ClientSecret string `yaml:"client_secret" env:"WAKAPI_MAIL_MAILWHALE_CLIENT_SECRET"`
}

type SMTPMailConfig struct {
	Host     string `env:"WAKAPI_MAIL_SMTP_HOST"`
	Port     uint   `env:"WAKAPI_MAIL_SMTP_PORT"`
	Username string `env:"WAKAPI_MAIL_SMTP_USER"`
	Password string `env:"WAKAPI_MAIL_SMTP_PASS"`
	TLS      bool   `env:"WAKAPI_MAIL_SMTP_TLS"`
}

type Config struct {
	Env            string `default:"dev" env:"ENVIRONMENT"`
	Version        string `yaml:"-"`
	QuickStart     bool   `yaml:"quick_start" env:"WAKAPI_QUICK_START"`
	SkipMigrations bool   `yaml:"skip_migrations" env:"WAKAPI_SKIP_MIGRATIONS"`
	InstanceId     string `yaml:"-"` // only temporary, changes between runs
	EnablePprof    bool   `yaml:"enable_pprof" env:"WAKAPI_ENABLE_PPROF"`
	App            appConfig
	Security       securityConfig
	Db             dbConfig
	Server         serverConfig
	Subscriptions  subscriptionsConfig
	Sentry         sentryConfig
	Mail           mailConfig
}

func (c *Config) CreateCookie(name, value string) *http.Cookie {
	return c.createCookie(name, value, c.Server.BasePath, c.Security.CookieMaxAgeSec)
}

func (c *Config) GetClearCookie(name string) *http.Cookie {
	return c.createCookie(name, "", c.Server.BasePath, -1)
}

func (c *Config) createCookie(name, value, path string, maxAge int) *http.Cookie {
	return &http.Cookie{
		Name:     name,
		Value:    value,
		Path:     path,
		MaxAge:   maxAge,
		Secure:   !c.Security.InsecureCookies,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	}
}

func (c *Config) IsDev() bool {
	return IsDev(c.Env)
}

func (c *Config) UseTLS() bool {
	return c.Server.TlsCertPath != "" && c.Server.TlsKeyPath != ""
}

func (c *appConfig) GetCustomLanguages() map[string]string {
	return utils.CloneStringMap(c.CustomLanguages, false)
}

func (c *appConfig) GetLanguageColors() map[string]string {
	return utils.CloneStringMap(c.Colors["languages"], true)
}

func (c *appConfig) GetEditorColors() map[string]string {
	return utils.CloneStringMap(c.Colors["editors"], true)
}

func (c *appConfig) GetOSColors() map[string]string {
	return utils.CloneStringMap(c.Colors["operating_systems"], true)
}

func (c *appConfig) GetAggregationTimeCron() string {
	if strings.Contains(c.AggregationTime, ":") {
		// old gocron format, e.g. "15:04"
		timeParts := strings.Split(c.AggregationTime, ":")
		h, err := strconv.Atoi(timeParts[0])
		if err != nil {
			logbuch.Fatal(err.Error())
		}

		m, err := strconv.Atoi(timeParts[1])
		if err != nil {
			logbuch.Fatal(err.Error())
		}

		return fmt.Sprintf("0 %d %d * * *", m, h)
	}

	return utils.CronPadToSecondly(c.AggregationTime)
}

func (c *appConfig) GetWeeklyReportCron() string {
	if strings.Contains(c.ReportTimeWeekly, ",") {
		// old gocron format, e.g. "fri,18:00"
		split := strings.Split(c.ReportTimeWeekly, ",")
		weekday := utils.ParseWeekday(split[0])
		timeParts := strings.Split(split[1], ":")

		h, err := strconv.Atoi(timeParts[0])
		if err != nil {
			logbuch.Fatal(err.Error())
		}

		m, err := strconv.Atoi(timeParts[1])
		if err != nil {
			logbuch.Fatal(err.Error())
		}

		return fmt.Sprintf("0 %d %d * * %d", m, h, weekday)
	}

	return utils.CronPadToSecondly(c.ReportTimeWeekly)
}

func (c *appConfig) GetLeaderboardGenerationTimeCron() []string {
	crons := []string{}

	var parse func(string) string

	if strings.Contains(c.LeaderboardGenerationTime, ":") {
		// old gocron format, e.g. "15:04"
		parse = func(s string) string {
			timeParts := strings.Split(s, ":")
			h, err := strconv.Atoi(timeParts[0])
			if err != nil {
				logbuch.Fatal(err.Error())
			}

			m, err := strconv.Atoi(timeParts[1])
			if err != nil {
				logbuch.Fatal(err.Error())
			}

			return fmt.Sprintf("0 %d %d * * *", m, h)
		}
	} else {
		parse = func(s string) string {
			return utils.CronPadToSecondly(s)
		}
	}

	for _, s := range utils.SplitMulti(c.LeaderboardGenerationTime, ",", ";") {
		crons = append(crons, parse(strings.TrimSpace(s)))
	}

	return crons
}

func (c *appConfig) HeartbeatsMaxAge() time.Duration {
	d, _ := time.ParseDuration(c.HeartbeatMaxAge)
	return d
}

func (c *securityConfig) ParseTrustReverseProxyIPs() {
	c.trustReverseProxyIpParsed = make([]net.IP, 0)
	for _, ip := range strings.Split(c.TrustReverseProxyIps, ",") {
		if parsedIp := net.ParseIP(strings.TrimSpace(ip)); parsedIp == nil {
			logbuch.Warn("failed to parse reverse proxy ip")
		} else {
			c.trustReverseProxyIpParsed = append(c.trustReverseProxyIpParsed, parsedIp)
		}
	}
}

func (c *securityConfig) TrustReverseProxyIPs() []net.IP {
	return c.trustReverseProxyIpParsed
}

func (c *dbConfig) IsSQLite() bool {
	return c.Dialect == "sqlite3"
}

func (c *dbConfig) IsMySQL() bool {
	return c.Dialect == "mysql"
}

func (c *dbConfig) IsPostgres() bool {
	return c.Dialect == "postgres"
}

func (c *serverConfig) GetPublicUrl() string {
	return strings.TrimSuffix(c.PublicUrl, "/")
}

func (c *SMTPMailConfig) ConnStr() string {
	return fmt.Sprintf("%s:%d", c.Host, c.Port)
}

func IsDev(env string) bool {
	return env == "dev" || env == "development"
}

func readColors() map[string]map[string]string {
	// Read language colors
	// Source:
	// - https://raw.githubusercontent.com/ozh/github-colors/master/colors.json
	// - https://wakatime.com/colors/operating_systems
	// - https://wakatime.com/colors/editors
	// Extracted from Wakatime website with XPath (see below) and did a bit of regex magic after.
	// - $x('//span[@class="editor-icon tip"]/@data-original-title').map(e => e.nodeValue)
	// - $x('//span[@class="editor-icon tip"]/div[1]/text()').map(e => e.nodeValue)

	raw := data.ColorsFile
	if IsDev(env) {
		raw, _ = os.ReadFile("data/colors.json")
	}

	var colors = make(map[string]map[string]string)
	if err := json.Unmarshal(raw, &colors); err != nil {
		logbuch.Fatal(err.Error())
	}

	return colors
}

func resolveDbDialect(dbType string) string {
	if dbType == "cockroach" {
		return "postgres"
	}
	if dbType == "sqlite" {
		return "sqlite3"
	}
	if dbType == "mariadb" {
		return "mysql"
	}
	return dbType
}

func Set(config *Config) {
	cfg = config
}

func Get() *Config {
	return cfg
}

func Load(configFlag string, version string) *Config {
	config := &Config{}

	if err := configor.New(&configor.Config{}).Load(config, configFlag); err != nil {
		logbuch.Fatal("failed to read config: %v", err)
	}

	env = config.Env

	config.Version = strings.TrimSpace(version)
	tagVersionMatch, _ := regexp.MatchString(`\d+\.\d+\.\d+`, version)
	if tagVersionMatch {
		config.Version = "v" + config.Version
	}

	config.InstanceId = uuid.NewV4().String()
	config.App.Colors = readColors()
	config.Db.Dialect = resolveDbDialect(config.Db.Type)

	hashKey := securecookie.GenerateRandomKey(64)
	blockKey := securecookie.GenerateRandomKey(32)
	sessionKey := securecookie.GenerateRandomKey(32)

	if IsDev(env) {
		logbuch.Warn("using temporary keys to sign and encrypt cookies in dev mode, make sure to set env to production for real-world use")
		hashKey, blockKey = getTemporarySecureKeys()
		blockKey = hashKey
	}

	config.Security.SecureCookie = securecookie.New(hashKey, blockKey)
	config.Security.SessionKey = sessionKey
	config.Security.ParseTrustReverseProxyIPs()

	config.Server.BasePath = strings.TrimSuffix(config.Server.BasePath, "/")

	for k, v := range config.App.CustomLanguages {
		if v == "" {
			config.App.CustomLanguages[k] = "unknown"
		}
	}

	if config.Sentry.Dsn != "" {
		logbuch.Info("enabling sentry integration")
		initSentry(config.Sentry, config.IsDev())
	}

	if config.App.DataRetentionMonths <= 0 {
		logbuch.Info("disabling data retention policy, keeping data forever")
	} else {
		dataRetentionWarning := fmt.Sprintf("⚠️ data retention policy will cause user data older than %d months to be deleted", config.App.DataRetentionMonths)
		if config.Subscriptions.Enabled {
			dataRetentionWarning += " (except for users with active subscriptions)"
		}
		logbuch.Warn(dataRetentionWarning)
	}

	// some validation checks
	if config.Server.ListenIpV4 == "-" && config.Server.ListenIpV6 == "-" && config.Server.ListenSocket == "" {
		logbuch.Fatal("either of listen_ipv4 or listen_ipv6 or listen_socket must be set")
	}
	if config.Db.MaxConn <= 0 {
		logbuch.Fatal("you must allow at least one database connection")
	}
	if config.Db.MaxConn > 1 && config.Db.IsSQLite() {
		logbuch.Warn("with sqlite, only a single connection is supported") // otherwise 'PRAGMA foreign_keys=ON' would somehow have to be set for every connection in the pool
		config.Db.MaxConn = 1
	}
	if config.Mail.Provider != "" && utils.FindString(config.Mail.Provider, emailProviders, "") == "" {
		logbuch.Fatal("unknown mail provider '%s'", config.Mail.Provider)
	}
	if _, err := time.ParseDuration(config.App.HeartbeatMaxAge); err != nil {
		logbuch.Fatal("invalid duration set for heartbeat_max_age")
	}
	if config.Security.TrustedHeaderAuth && len(config.Security.trustReverseProxyIpParsed) == 0 {
		config.Security.TrustedHeaderAuth = false
	}

	cronParser := cron.NewParser(cron.Second | cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor)

	if _, err := cronParser.Parse(config.App.GetWeeklyReportCron()); err != nil {
		logbuch.Fatal("invalid cron expression for report_time_weekly")
	}
	if _, err := cronParser.Parse(config.App.GetAggregationTimeCron()); err != nil {
		logbuch.Fatal("invalid cron expression for aggregation_time")
	}
	for _, c := range config.App.GetLeaderboardGenerationTimeCron() {
		if _, err := cronParser.Parse(c); err != nil {
			logbuch.Fatal("invalid cron expression for leaderboard_generation_time")
		}
	}

	// deprecation notices
	if strings.Contains(config.App.AggregationTime, ":") {
		logbuch.Warn("you're using deprecated syntax for 'aggregation_time', please change it to a valid cron expression")
	}
	if strings.Contains(config.App.ReportTimeWeekly, ":") {
		logbuch.Warn("you're using deprecated syntax for 'report_time_weekly', please change it to a valid cron expression")
	}
	if strings.Contains(config.App.LeaderboardGenerationTime, ":") {
		logbuch.Warn("you're using deprecated syntax for 'leaderboard_generation_time', please change it to a semicolon-separated list if valid cron expressions")
	}

	Set(config)
	return Get()
}

func Empty() *Config {
	return &Config{
		App:           appConfig{},
		Security:      securityConfig{},
		Db:            dbConfig{},
		Server:        serverConfig{},
		Subscriptions: subscriptionsConfig{},
		Sentry:        sentryConfig{},
		Mail:          mailConfig{},
	}
}
