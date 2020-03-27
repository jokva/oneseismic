package server

import (
	"fmt"
	"net/url"
	"strconv"
)

type errInvalidConfig struct {
	Name string
	Err  error
}

func (e *errInvalidConfig) Unwrap() error { return e.Err }
func (e *errInvalidConfig) Error() string { return fmt.Sprintf(e.Name) }

type Config struct {
	Profiling         bool
	AuthServer        url.URL
	HostAddr          string
	Issuer            string
	StitchGrpcAddr    string
	AzureBlobSettings AzureBlobSettings
	ResourceID        string
	LogDBConnStr      string
	APISecret         string
}

func orDefaultBool(val string, def bool) bool {
	if val, err := strconv.ParseBool(val); err == nil {
		return val
	}

	return def
}

func azb(m map[string]string) AzureBlobSettings {
	return AzureBlobSettings{
		StorageURL:  m["AZURE_STORAGE_URL"],
		AccountName: m["AZURE_STORAGE_ACCOUNT"],
		AccountKey:  m["AZURE_STORAGE_ACCESS_KEY"],
	}
}

func ParseConfig(m map[string]string) (*Config, error) {
	authServer, err := url.ParseRequestURI(m["AUTHSERVER"])
	if err != nil {
		return nil, &errInvalidConfig{Name: "Invalid AUTHSERVER", Err: err}
	}

	apiSecret, err := verifyAPISecret(m["API_SECRET"])
	if err != nil {
		return nil, err
	}

	conf := &Config{
		APISecret:         *apiSecret,
		AuthServer:        *authServer,
		AzureBlobSettings: azb(m),
		HostAddr:          m["HOST_ADDR"],
		Issuer:            m["ISSUER"],
		LogDBConnStr:      m["LOGDB_CONNSTR"],
		Profiling:         orDefaultBool(m["PROFILING"], false),
		ResourceID:        m["RESOURCE_ID"],
		StitchGrpcAddr:    m["STITCH_GRPC_ADDR"],
	}

	return conf, nil
}

func verifyAPISecret(sec string) (*string, error) {
	if len(sec) < 8 {
		return nil, &errInvalidConfig{"Invalid API_SECRET", fmt.Errorf("len(%s) == %d < 8", sec, len(sec))}
	}

	return &sec, nil
}