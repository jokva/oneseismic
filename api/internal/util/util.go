package util

import (
	"compress/gzip"
	"context"
	"fmt"
	"io/ioutil"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/equinor/oneseismic/api/internal/auth"
	"github.com/equinor/oneseismic/api/internal/message"
	"github.com/Azure/azure-storage-blob-go/azblob"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

func MakePID() string {
	return uuid.New().String()
}

func GeneratePID(ctx *gin.Context) {
	ctx.Set("pid", MakePID())
}

type gzipWriter struct {
	gin.ResponseWriter
	writer *gzip.Writer
}

func (gz *gzipWriter) Write(b []byte) (int, error) {
	return gz.writer.Write(b)
}

// Compress the response if requested with a ?compression=kind query
//
// The implementation is roughly based on https://github.com/gin-contrib/gzip/
// with a couple of changed assumptions. The gin-contrib/gzip does not quite
// fit our usecase, and is very much geared towards compressing small
// text-responses and serving files, in a proper webserver fashion.
func Compression() gin.HandlerFunc {
	// responses are not very compressible (usually compressible to half the
	// size), and *speed* is the key anyway. Within the data centre it seems
	// like the break-even for compression time vs. saved transport cost is at
	// approx 20M responses.
	//
	// From a small rough experiment on a 14M response built from a 1.4M
	// response concatenated 10 times indicates that there are no significant
	// size improvements, but huge runtime costs in upping the compression
	// level:
	//
	// $ time gzip -1 -c response.bin | wc -c
	// 5812130
	// real    0m0.286s
	// $ time gzip -6 -c response.bin | wc -c
	// 5330006
	// real    0m1.230s

	// make new writers from a pool. There is some overhead in creating new
	// gzip writers, and they're easily re-usable. Using a sync.pool seems to
	// be the standard implementation for this.
	//
	// https://github.com/gin-contrib/gzip/blob/7bbc855cce8a575268c8f3e8d0f7a6a67f3dee65/handler.go#L22
	gzpool := sync.Pool {
		New: func() interface {} {
			gz, err := gzip.NewWriterLevel(ioutil.Discard, 1)
			if err != nil {
				panic(err)
			}
			return gz
		},
	}

	// It is very important that ctx.Next() is called - it effectively suspends
	// this handler and performs the request, then resumes where it left off.
	// It ensures that the Close(), Reset() and Put() are performed *after*
	// everything is properly written, and resources can be cleaned up.
	return func (ctx *gin.Context) {
		if (ctx.Query("compression") == "gz") {
			gz := gzpool.Get().(*gzip.Writer)
			defer gzpool.Put(gz)
			defer gz.Reset(ioutil.Discard)
			defer gz.Close()

			gz.Reset(ctx.Writer)
			ctx.Writer = &gzipWriter{ctx.Writer, gz}
			ctx.Header("Content-Encoding", "gzip")
			ctx.Next()

			if (ctx.GetHeader("Transfer-Encoding") != "chunked") {
				ctx.Header("Content-Length", fmt.Sprint(ctx.Writer.Size()))
			}
		}
	}
}

/*
 * Get the manifest for the cube from the blob store.
 *
 * It's important that this is a blocking read, since this is the first
 * authorization mechanism in oneseismic. If the user (through the
 * on-behalf-token) does not have permissions to read the manifest, it
 * shouldn't be able to read the cube either. If so, no more processing should
 * be done, and the request discarded.
 */
func FetchManifest(
	ctx context.Context,
	token string,
	containerURL *url.URL,
) ([]byte, error) {
	credentials := azblob.NewTokenCredential(token, nil)
	pipeline    := azblob.NewPipeline(credentials, azblob.PipelineOptions{})
	container   := azblob.NewContainerURL(*containerURL, pipeline)
	blob        := container.NewBlobURL("manifest.json")

	dl, err := blob.Download(
		ctx,
		0, /* offset */
		azblob.CountToEnd,
		azblob.BlobAccessConditions {},
		false, /* content-get-md5 */
		azblob.ClientProvidedKeyOptions {},
	)
	if err != nil {
		return nil, err
	}

	body := dl.Body(azblob.RetryReaderOptions{})
	defer body.Close()
	return ioutil.ReadAll(body)
}

/*
 * Centralize the understanding of error conditions of azblob.download.
 *
 * There are two classes of errors:
 * 1. A hard failure, i.e. the request itself failed, such as network
 *    conditions.
 * 2. A soft failure, i.e. the request itself suceeded, but was without
 *    sufficient permissions, wrong syntax or similar. azblob probably handles
 *    this by parsing the status code and maybe the response body.
 *
 * Most calls to FetchManifest() should probably immediately call this function
 * on error and exit, but it's moved into its own function so that error paths
 * can be emulated and tested independently. An added benefit is that should a
 * function, for some reason, need FetchManifest() but want custom error+abort
 * handling, it is sufficient to implement bespoke error handling and simply
 * not call this.
 */
func AbortOnManifestError(ctx *gin.Context, err error) {
	switch e := err.(type) {
	case azblob.StorageError:
		/*
		 * Request successful, but the service returned some error e.g. a
		 * non-existing cube, unauthorized request.
		 *
		 * For now, just write the status-text into the body, which should be
		 * slightly more accurate than just the status code. Once the interface
		 * matures, this should probably be a more structured error message.
		 */
		sc := e.Response()
		ctx.String(sc.StatusCode, sc.Status)
		ctx.Abort()
	default:
		/*
		 * We don't care if the error occured is a networking error, faulty
		 * logic or something else - from the user side this is an
		 * InternalServerError regardless. At some point in the future, we might
		 * want to deal with particular errors here.
		 */
		ctx.AbortWithStatus(http.StatusInternalServerError)
	}
}

func ParseManifest(doc []byte) (*message.Manifest, error) {
	m := message.Manifest{}
	return m.Unpack(doc)
}

// This function is pure automation
//
// So checking if a token is valid, has the right permissions, not-yet-expired
// and actually works is something we don't want to do - it's error prone,
// mistakes mean security vulnerabilities, and it's plain ugly. At the same
// time we need to cache, because requesting fresh tokens every time is *very*
// slow (400ms or more added to each request).
//
// To handle this, the auth.Tokens implementation can cache for us, but it
// cannot know what we want to use to the token for. That's where this function
// kicks in - it automates getting a token (maybe from cache, maybe fresh),
// calling a function and checking the error, and retrying once should it fail.
//
// If the failure is simply a revoked or expired token, the second call should
// be successful. If it's bad permissions or similar then the second call
// should also fail. This means bad requests are significantly slower, but they
// should be reasonably rare.
//
// Effectively, this is a way to evict old cache entries, and using this helper
// should be preferred to manually retrying. It does come with some complexity
// though - generic code in go is not pretty (hello, interface{}), but the
// alternative is the structurally repetitive try-then-maybe-retry which I
// already messed up once. It also mixes errors from GetOnbehalf and the actual
// function, so callers that really care about error specifics must handle
// both.
//
// The fn callback should take an on-behalf token as a parameter.
//
// Example use:
//
// ctx := context.Background()
// endpoint := "https://acc.storage.com/"
// authorization := ctx.GetHeader("Authorization")
// cubes, err := util.WithOnbehalfAndRetry(
// 	tokens,
// 	authorization,
// 	function (tok string) (interface{}, error) {
// 		return list(ctx, endpoint, tok)
//  }
// )
func WithOnbehalfAndRetry(
	tokens auth.Tokens,
	auth   string,
	fn     func(string) (interface{}, error),
) (interface{}, error) {
	token, err := tokens.GetOnbehalf(auth)
	if err != nil {
		return nil, err
	}
	v, err := fn(token)
	if err == nil {
		return v, nil
	}

	tokens.Invalidate(auth)
	token, err = tokens.GetOnbehalf(auth)
	if err != nil {
		return nil, err
	}
	return fn(token)
}

/*
 * Custom logger for the /query family of endpoints, that logs the id of the
 * process to be generated by the request (pid).
 */
func QueryLogger(ctx *gin.Context) {
	gin.LoggerWithFormatter(func(param gin.LogFormatterParams) string {
		return fmt.Sprintf("%s - [%s] \"pid=%s, %s %s %s %d %s %s\"\n",
			param.ClientIP,
			param.TimeStamp.Format(time.RFC1123),
			param.Keys["pid"],
			param.Method,
			param.Path,
			param.Request.Proto,
			param.StatusCode,
			param.Latency,
			param.ErrorMessage,
		)
	})(ctx)
}

/*
 * List the cubes in a storage account
 *
 * Please note that while its phrased as something particular, "list cubes"
 * really boils down to getting the names of the containers in a particular
 * account. It is assumed that a storage account is used for oneseismic, and
 * oneseismic only, in which case every container should correspond to a cube.
 *
 * It is untested, but likely, that containers with permissions set to
 * non-readable for the caller will not show up in this list, which is the
 * intention.
 */
func ListCubes(
	ctx      context.Context,
	endpoint *url.URL, // typically https://<account>.blob.core.windows.net
	token    string,
) ([]string, error) {
	credentials := azblob.NewTokenCredential(token, nil)
	pipeline    := azblob.NewPipeline(credentials, azblob.PipelineOptions{})
	storageacc  := azblob.NewServiceURL(*endpoint, pipeline)

	cubes := make([]string, 0)
	for marker := (azblob.Marker{}); marker.NotDone(); {
		xs, err := storageacc.ListContainersSegment(
			ctx,
			marker,
			azblob.ListContainersSegmentOptions{},
		)
		if err != nil {
			return nil, err
		}
		for _, cube := range xs.ContainerItems {
			cubes = append(cubes, cube.Name)
		}
		marker = xs.NextMarker
	}
	return cubes, nil
}
