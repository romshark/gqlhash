// Command api is the sample GraphQL API of the playground. It answers from a
// fixed set of users, so the playground has something real to talk to without a
// database.
//
// It's a module of its own, so its dependencies stay out of gqlhash.
package main

import (
	"context"
	"flag"
	"log"
	"net/http"

	graphql "github.com/graph-gophers/graphql-go"
	"github.com/graph-gophers/graphql-go/relay"
)

const schemaText = `
	schema {
		query: Query
		mutation: Mutation
	}

	type Query {
		user(id: ID!): User
		users(limit: Int = 10): [User!]!
	}

	type Mutation {
		deleteUser(id: ID!): Boolean!
	}

	type User {
		id: ID!
		name: String!
		email: String!
	}
`

type user struct {
	id, name, email string
}

var users = []user{
	{"1", "Ada Lovelace", "ada@example.com"},
	{"2", "Alan Turing", "alan@example.com"},
	{"3", "Grace Hopper", "grace@example.com"},
}

type resolver struct{}

func (*resolver) User(_ context.Context, args struct{ ID graphql.ID }) *userResolver {
	for i := range users {
		if graphql.ID(users[i].id) == args.ID {
			return &userResolver{users[i]}
		}
	}
	return nil
}

// Limit is no pointer: the schema gives it a default, so it always has a value.
func (*resolver) Users(_ context.Context, args struct{ Limit int32 }) []*userResolver {
	limit := len(users)
	if int(args.Limit) < limit {
		limit = int(args.Limit)
	}
	if limit < 0 {
		limit = 0
	}
	out := make([]*userResolver, 0, limit)
	for i := range users[:limit] {
		out = append(out, &userResolver{users[i]})
	}
	return out
}

// DeleteUser is here to be rejected by the proxy. It changes nothing.
func (*resolver) DeleteUser(_ context.Context, args struct{ ID graphql.ID }) bool {
	log.Printf("deleteUser(%s) reached the API", args.ID)
	return true
}

type userResolver struct{ u user }

func (r *userResolver) ID() graphql.ID { return graphql.ID(r.u.id) }
func (r *userResolver) Name() string   { return r.u.name }
func (r *userResolver) Email() string  { return r.u.email }

func main() {
	listen := flag.String("listen", ":4000", "Address to listen on")
	flag.Parse()

	schema := graphql.MustParseSchema(schemaText, &resolver{})
	mux := http.NewServeMux()
	mux.Handle("/graphql", &relay.Handler{Schema: schema})
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok"))
	})

	log.Printf("sample GraphQL API listening on %s/graphql", *listen)
	server := &http.Server{Addr: *listen, Handler: mux}
	log.Fatal(server.ListenAndServe())
}
