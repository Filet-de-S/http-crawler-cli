package grpc_client

import (
	"crawler/uniqURL_db"
	"golang.org/x/net/context"
	"google.golang.org/grpc"
)

type store struct {
	client ParsedClient
	clConn *grpc.ClientConn
}

func New(addr, certFile string) (uniqURL_db.Parsed, error) {
	var err error
	var clConn *grpc.ClientConn
	dialOpts := []grpc.DialOption{grpc.WithBlock(), grpc.WithInsecure()}

	//if certFile != "" {
	//	creds, err := credentials.NewClientTLSFromFile(certFile, "")
	//	if err != nil {
	//		return nil, err
	//	}
	//
	//	fmt.Println(creds.Info())
	//	dialOpts = append(dialOpts, grpc.WithTransportCredentials(creds))
	//}

	clConn, err = grpc.Dial(addr, dialOpts...)
	if err != nil {
		return nil, err
	}

	return &store{NewParsedClient(clConn), clConn}, nil
}

func (s *store) Get(ctx context.Context, url string) (depth int32, exists bool, err error) {
	r, err := s.client.Get(ctx, &GetURL{Url: url})
	if err != nil {
		return 0, false, err
	}
	return r.Depth, r.Exists, err
}

func (s *store) Save(ctx context.Context, url uniqURL_db.URL, depth int32) error {
	_, err := s.client.Save(ctx, &SaveURL{
		Url:   url,
		Depth: depth,
	})
	return err
}

func (s *store) Close() error {
	return s.clConn.Close()
}
