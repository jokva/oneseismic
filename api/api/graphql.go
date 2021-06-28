package api

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"

	"github.com/gin-gonic/gin"
	graphql "github.com/graph-gophers/graphql-go"
	"github.com/go-redis/redis/v8"
	"github.com/Azure/azure-storage-blob-go/azblob"

	"github.com/equinor/oneseismic/api/internal/auth"
	"github.com/equinor/oneseismic/api/internal/message"
	"github.com/equinor/oneseismic/api/internal/util"
)

type gql struct {
	schema *graphql.Schema
}

type resolver struct {
	BasicEndpoint
}
type cube struct {
	id       graphql.ID
	root     *resolver
	manifest *message.Manifest
}
type promise struct {
	url string
	key string
}

func (r *resolver) Cubes(ctx context.Context) ([]graphql.ID, error) {
	keys := ctx.Value("keys").(map[string]string)
	pid  := keys["pid"]
	auth := keys["Authorization"]

	endpoint, err := url.Parse(r.endpoint)
	if err != nil {
		log.Printf("pid=%s %v", pid, err)
		return []graphql.ID{}, err
	}

	cubes, err := util.WithOnbehalfAndRetry(
		r.tokens,
		auth,
		func (tok string) (interface{}, error) {
			return util.ListCubes(ctx, endpoint, tok)
		},
	)
	if err != nil {
		log.Printf("pid=%s, %v", pid, err)
		return []graphql.ID{}, err
	}

	guids := cubes.([]string)
	list := make([]graphql.ID, len(guids))
	for i, id := range guids {
		list[i] = graphql.ID(id)
	}
	return list, nil
}

func (s *resolver) MakeSliceTask(
	pid       string,
	token     string,
	manifest  []byte,
	shape     []int32,
	shapecube []int32,
	guid      string,
	dim       int,
	lineno    int,
) *message.Task {
	task := s.BasicEndpoint.MakeTask(
		pid,
		guid,
		token,
		manifest,
		shape,
		shapecube,
	)
	task.Function = "slice"
	task.Params   = &message.SliceParams {
		Dim:    dim,
		Lineno: lineno,
	}
	return task
}

func (c *resolver) MakeCurtainTask(
	pid       string,
	guid      string,
	token     string,
	manifest  []byte,
	shape     []int32,
	shapecube []int32,
	params    *message.CurtainParams,
) *message.Task {
	task := c.BasicEndpoint.MakeTask(
		pid,
		guid,
		token,
		manifest,
		shape,
		shapecube,
	)
	task.Function = "curtain"
	task.Params   = params
	return task
}

func (r *resolver) Cube(
	ctx context.Context,
	args struct { Id graphql.ID },
) (*cube, error) {
	keys := ctx.Value("keys").(map[string]string)
	pid  := keys["pid"]
	auth := keys["Authorization"]

	manifest, err := getManifest(
		ctx,
		r.tokens,
		r.endpoint,
		string(args.Id),
		auth,
	)
	if err != nil {
		log.Printf("pid=%s %v", pid, err)
		return nil, err
	}

	return &cube {
		id:       args.Id,
		root:     r,
		manifest: manifest,
	}, nil
}

func (c *cube) Id() graphql.ID {
	return c.id
}

type sliceargs struct {
	Kind int32
	Id   int32
}

/*
 * This is the util.GetManifest function, but tuned for graphql and with
 * gin-specifics removed. Its purpose is to make for a quick migration to a
 * working graphql interface to oneseismic. Expect this function to be removed
 * or drastically change soon.
 */
func getManifest(
	ctx      context.Context,
	tokens   auth.Tokens,
	endpoint string,
	guid     string,
	auth     string,
) (*message.Manifest, error) {
	container, err := url.Parse(fmt.Sprintf("%s/%s", endpoint, guid))
	if err != nil {
		return nil, err
	}

	manifest, err := util.WithOnbehalfAndRetry(
		tokens,
		auth,
		func (tok string) (interface{}, error) {
			return util.FetchManifest(ctx, tok, container)
		},
	)
	if err != nil {
		switch e := err.(type) {
		case azblob.StorageError:
			sc := e.Response()
			if sc.StatusCode == http.StatusNotFound {
				// TODO: add guid as a part of the error message?
				return nil, errors.New("Not found")
			}
			return nil, errors.New("Internal error")
		}
		return nil, err
	}

	m, err := util.ParseManifest(manifest.([]byte))
	if err != nil {
		return nil, err
	}
	return m, nil
}

func (c *cube) Slice(
	ctx context.Context,
	args sliceargs,
) (*promise, error) {
	keys := ctx.Value("keys").(map[string]string)
	pid  := keys["pid"]
	auth := keys["Authorization"]
	/*
	 * Embedding a json doc as a string works (surprisingly) well, since the
	 * Pack()/encoding escapes all nested quotes. It might be reasonable at
	 * some point to change the underlying representation to messagepack, or
	 * even send the messages gzipped, but so for now strings and embedded
	 * documents should do fine.
	 *
	 * This opens an opportunity for the manifest forwarded not being quite
	 * faithful to what's stored in blob, i.e. information can be stripped out
	 * or added.
	 */
	token, err := c.root.tokens.GetOnbehalf(auth)
	if err != nil {
		// No further recovery is tried - GetManifest should already have fixed
		// a broken token, so this should be readily cached. If it is
		// just-about to expire then the process will fail pretty soon anyway,
		// so just give up.
		log.Printf("pid=%s, %v", pid, err)
		return nil, err
	}

	packedmanifest, err := c.manifest.Pack()
	if err != nil {
		log.Printf("pid=%s %v", pid, err)
		return nil, err
	}

	cubeshape := make([]int32, 0, len(c.manifest.Dimensions))
	for i := 0; i < len(c.manifest.Dimensions); i++ {
		cubeshape = append(cubeshape, int32(len(c.manifest.Dimensions[i])))
	}
	msg := c.root.MakeSliceTask(
		pid,
		token,
		packedmanifest,
		[]int32{ 64, 64, 64 },
		cubeshape,
		string(c.id),
		int(args.Kind),
		int(args.Id),
	)

	key, err := c.root.keyring.Sign(pid)
	if err != nil {
		log.Printf("pid=%s, %v", pid, err)
		return nil, err
	}

	query, err := c.root.sched.MakeQuery(msg)
	if err != nil {
		log.Printf("pid=%s, %v", pid, err)
		return nil, err
	}

	go func () {
		err := c.root.sched.Schedule(context.Background(), pid, query)
		if err != nil {
			/*
			 * Make scheduling errors fatal to detect them for debugging.
			 * Eventually this should log, maybe cancel the process, and
			 * continue.
			 */
			log.Fatalf("pid=%s, %v", pid, err)
		}
	}()

	return &promise {
		url: fmt.Sprintf("result/%s", pid),
		key: key,
	}, nil
}

func toCurtainParams(coords [][]int32) *message.CurtainParams {
	// TODO: sanity check etc
	xs := make([]int, len(coords))
	ys := make([]int, len(coords))
	for i, xy := range coords {
		xs[i] = int(xy[0])
		ys[i] = int(xy[1])
	}
	return &message.CurtainParams{ Dim0s: xs, Dim1s: ys }
}

func (c *cube) Curtain(
	ctx    context.Context,
	args   struct { Coords [][]int32 },
) (*promise, error) {
	keys := ctx.Value("keys").(map[string]string)
	pid  := keys["pid"]
	auth := keys["Authorization"]

	token, err := c.root.tokens.GetOnbehalf(auth)
	if err != nil {
		// No further recovery is tried - GetManifest should already have fixed
		// a broken token, so this should be readily cached. If it is
		// just-about to expire then the process will fail pretty soon anyway,
		// so just give up.
		log.Printf("pid=%s, %v", pid, err)
		return nil, err
	}

	packedmanifest, err := c.manifest.Pack()
	if err != nil {
		log.Printf("pid=%s %v", pid, err)
		return nil, err
	}

	cubeshape := make([]int32, 0, len(c.manifest.Dimensions))
	for i := 0; i < len(c.manifest.Dimensions); i++ {
		cubeshape = append(cubeshape, int32(len(c.manifest.Dimensions[i])))
	}
	msg := c.root.MakeCurtainTask(
		pid,
		string(c.id),
		token,
		packedmanifest,
		[]int32{ 64, 64, 64 },
		cubeshape,
		toCurtainParams(args.Coords),
	)

	key, err := c.root.keyring.Sign(pid)
	if err != nil {
		log.Printf("pid=%s, %v", pid, err)
		return nil, err
	}

	query, err := c.root.sched.MakeQuery(msg)
	if err != nil {
		log.Printf("pid=%s, %v", pid, err)
		return nil, err
	}

	go func () {
		err := c.root.sched.Schedule(context.Background(), pid, query)
		if err != nil {
			/*
			 * Make scheduling errors fatal to detect them for debugging.
			 * Eventually this should log, maybe cancel the process, and
			 * continue.
			 */
			log.Fatalf("pid=%s, %v", pid, err)
		}
	}()

	return &promise {
		url: fmt.Sprintf("result/%s", pid),
		key: key,
	}, nil
}

func (p *promise) Url() string {
	return p.url
}

func (p *promise) Key() string {
	return p.key
}

func MakeGraphQL(
	keyring  *auth.Keyring,
	endpoint string,
	storage  redis.Cmdable,
	tokens   auth.Tokens,
) *gql {
	schema := `
type Query {
    cubes: [ID!]!
    cube(id: ID!): Cube!
}

type Cube {
    id: ID!

    slice(kind: Int!, id: Int!): Promise!
	curtain(coords: [[Int!]!]!): Promise!
}

type Promise {
    url: String!
    key: String!
}
	`
	resolver := &resolver {
		MakeBasicEndpoint(
			keyring,
			endpoint,
			storage,
			tokens,
		),
	}


	s := graphql.MustParseSchema(schema, resolver)
	return &gql {
		schema: s,
	}
}

func (g *gql) Get(ctx *gin.Context) {
	query  := ctx.Query("query")
	opName := ctx.Query("operationName")

	// TODO: parse the ?variables=... to this map
	//variables := ctx.Query("variables")
	variables := make(map[string]interface{})
	ctx.JSON(200, g.execQuery(ctx, query, opName, variables))
}

func (g *gql) Post(ctx *gin.Context) {
	type body struct {
		Query         string                 `json:"query"`
		OperationName string                 `json:"operationName"`
		Variables     map[string]interface{} `json:"variables"`
	}
	b := body {}
	err := ctx.BindJSON(&b)
	if err != nil {
		log.Printf("pid=%s %v", ctx.GetString("pid"), err)
		return
	}

	ctx.JSON(200, g.execQuery(
		ctx,
		b.Query,
		b.OperationName,
		b.Variables,
	))
}

func (g *gql) execQuery(
	ctx    *gin.Context,
	query  string,
	opName string,
	variables map[string]interface{},
) *graphql.Response {
	keys := map[string]string {
		"pid": ctx.GetString("pid"),
		"Authorization": ctx.GetHeader("Authorization"),
	}
	c := context.WithValue(ctx, "keys", keys)
	return g.schema.Exec(c, query, opName, variables)
}
