package handlers

import (
	"html/template"
	"net/http"
	"strings"

	"github.com/0xrinful/InkFetch/internal/service"
)

type Auth struct {
	svc          *service.Service
	templates    map[string]*template.Template
	setSession   func(http.ResponseWriter, *http.Request, int64)
	clearSession func(http.ResponseWriter)
}

type authPageData struct {
	Error string
	Email string
}

func NewAuth(
	svc *service.Service,
	templates map[string]*template.Template,
	setSession func(http.ResponseWriter, *http.Request, int64),
	clearSession func(http.ResponseWriter),
) *Auth {
	return &Auth{
		svc:          svc,
		templates:    templates,
		setSession:   setSession,
		clearSession: clearSession,
	}
}

func (a *Auth) LoginPage(w http.ResponseWriter, r *http.Request) {
	renderTemplate(w, a.templates, "login.html", authPageData{})
}

func (a *Auth) LoginSubmit(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	email := strings.TrimSpace(r.FormValue("email"))
	password := r.FormValue("password")

	user, err := a.svc.SignIn(r.Context(), email, password)
	if err != nil {
		renderTemplate(
			w,
			a.templates,
			"login.html",
			authPageData{Error: "Invalid email or password.", Email: email},
		)
		return
	}

	a.setSession(w, r, int64(user.ID))
	http.Redirect(w, r, "/", http.StatusFound)
}

func (a *Auth) SignupPage(w http.ResponseWriter, r *http.Request) {
	renderTemplate(w, a.templates, "signup.html", authPageData{})
}

func (a *Auth) SignupSubmit(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	email := strings.TrimSpace(r.FormValue("email"))
	password := r.FormValue("password")

	if email == "" || len(password) < 4 {
		renderTemplate(w, a.templates, "signup.html", authPageData{
			Error: "Enter a valid email and a password of at least 4 characters.",
			Email: email,
		})
		return
	}

	user, err := a.svc.SignUp(r.Context(), email, password)
	if err != nil {
		message := "Unable to create account."
		if strings.Contains(err.Error(), "UNIQUE constraint failed: users.email") {
			message = "An account with that email already exists."
		}
		renderTemplate(w, a.templates, "signup.html", authPageData{Error: message, Email: email})
		return
	}

	a.setSession(w, r, int64(user.ID))
	http.Redirect(w, r, "/", http.StatusFound)
}

func (a *Auth) Logout(w http.ResponseWriter, r *http.Request) {
	a.clearSession(w)
	http.Redirect(w, r, "/login", http.StatusFound)
}
