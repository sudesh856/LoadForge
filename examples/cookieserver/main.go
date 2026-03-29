package main

import (
	"fmt"
	"log"
	"net/http"
)

func main() {

	http.HandleFunc("/login", func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{
			Name:  "session",
			Value: "abc123",
			Path:  "/",
		})
		w.WriteHeader(http.StatusOK)
		fmt.Fprintln(w, `{"status":"logged in"}`)
	})

	http.HandleFunc("/profile", func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie("session")
		if err != nil || cookie.Value == "" {
			w.WriteHeader(http.StatusUnauthorized)
			fmt.Fprintln(w, `{"error":"no session"}`)
			return
		}
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, `{"user":"suddpanzer","session":"%s"}`, cookie.Value)
	})

	log.Println("cookie test server listening on :8081")
	log.Fatal(http.ListenAndServe(":8081", nil))
}
