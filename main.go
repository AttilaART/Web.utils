package main

import (
	"bytes"
	"database/sql"
	"embed"
	"encoding/csv"
	"fmt"
	"html/template"
	"io"
	"log"
	"net/http"
	"os"
	"reflect"
	"strconv"
	"strings"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

//go:embed assets/*
//go:embed templates/*
var filesystem embed.FS

var (
	db         *sql.DB
	access_key string
)

type signed_bool int // use True, False and None
const (
	None  = -1
	True  = 1
	False = 0
)

type Person struct {
	Ticket_number string
	Has_ep_ticket bool
	Boarded_dep   bool
	Boarded_ret   bool
	Name          string
	Phone_number  string
	Email         string
	Class         string
	Coach         string
	Is_staff      bool
}

type People_list struct {
	People        []Person
	List_complete bool
	Only_data     bool
	Coaches       []string
	Classes       []string
}

type Valid_template_entry struct {
	Person                   Person
	Dep_marked_automatically bool
	Ret_marked_automatically bool
}

func check_port(p string) (string, error) {
	if p == "" {
		println("No port specified. Defaulting to 8080")
		return "8080", nil
	}
	_, err := strconv.Atoi(p)
	if err != nil {
		return p, err
	}

	return p, nil
}

func check_authorised(r *http.Request) bool {
	if access_key == "" {
		return true
	}

	token, err := r.Cookie("access_key")
	if err != nil {
		return false
	}
	return token.Value == access_key
}

func redirector(w http.ResponseWriter, r *http.Request, url string, code int) {
	t := template.Must(template.ParseFS(filesystem, "templates/redirect.html"))
	t.Execute(w, struct {
		Url  string
		Code string
	}{url, http.StatusText(code)})
}

func ticket_checker(w http.ResponseWriter, r *http.Request) {
	if !check_authorised(r) {
		redirector(w, r, "/ticket_checker/authorise/", http.StatusSeeOther)
		return
	}

	t := template.Must(template.ParseFS(filesystem, "templates/index.html", "templates/head.html"))

	t.Execute(w, nil)
}

func authorise(w http.ResponseWriter, r *http.Request) {
	if check_authorised(r) {
		redirector(w, r, "/ticket_checker/", http.StatusSeeOther)
		return
	}

	t := template.Must(template.ParseFS(filesystem, "templates/authorise.html", "templates/head.html"))
	t.Execute(w, false)
}

func revert_boarding(w http.ResponseWriter, r *http.Request) {
	if !check_authorised(r) {

		io.WriteString(w, "Unauthorised")
		return
	}
	ticket_number := r.FormValue("ticket_number")

	person := get_person(ticket_number)
	switch r.FormValue("Boarded_dep") {
	case "false":
		person.Boarded_dep = false
	case "true":
		person.Boarded_dep = true
	}
	switch r.FormValue("Boarded_ret") {
	case "false":
		person.Boarded_ret = false
	case "true":
		person.Boarded_ret = true
	}

	update_person(person)

	template_input := Valid_template_entry{Person: person, Dep_marked_automatically: false, Ret_marked_automatically: false}

	t := template.Must(template.ParseFS(filesystem, "templates/valid_ticket.html", "templates/reset_button.html"))
	t.Execute(w, template_input)
}

func check_access_key(w http.ResponseWriter, r *http.Request) {
	// check key
	given_key := r.FormValue("access_key")

	cookie, err := r.Cookie("access_key")
	if err == nil {
		if cookie.Value == access_key {
			redirector(w, r, "/ticket_checker/", http.StatusSeeOther)
			return
		}
	}

	if given_key != access_key {
		t := template.Must(template.ParseFS(filesystem, "templates/authorise.html", "templates/head.html"))
		t.Execute(w, true)
		return
	}

	access_key_cookie := http.Cookie{Name: "access_key", Value: access_key}

	http.SetCookie(w, &access_key_cookie)

	redirector(w, r, "/ticket_checker/", http.StatusSeeOther)
}

func check_ticket_number(n string) bool {
	var num int
	err := db.QueryRow("SELECT count(*) FROM Participants WHERE ticket_number=?", n).Scan(&num)
	if err != nil {
		panic(err)
	}

	return num >= 1
}

func get_coaches() []string {
	var coaches []string
	rows, err := db.Query("SELECT DISTINCT coach FROM Participants;")
	if err != nil {
		panic(err)
	}

	for rows.Next() {
		var coach string
		err := rows.Scan(&coach)
		if err != nil {
			panic(err)
		}
		coaches = append(coaches, coach)
	}

	return coaches
}

func get_classes() []string {
	var classses []string
	rows, err := db.Query("SELECT DISTINCT class FROM Participants ORDER BY class;")
	if err != nil {
		panic(err)
	}

	for rows.Next() {
		var class string
		err := rows.Scan(&class)
		if err != nil {
			panic(err)
		}
		classses = append(classses, class)
	}

	return classses
}

func get_person(n string) Person {
	var person Person
	err := db.QueryRow("SELECT * FROM Participants WHERE ticket_number=?", n).Scan(
		&person.Ticket_number,
		&person.Has_ep_ticket,
		&person.Boarded_dep,
		&person.Boarded_ret,
		&person.Name,
		&person.Phone_number,
		&person.Email,
		&person.Class,
		&person.Coach,
		&person.Is_staff,
	)
	if err != nil {
		panic(err)
	}

	return person
}

func get_people(search_name string, filter_dep signed_bool, filter_ret signed_bool, filter_coach string, filter_class string, is_staff signed_bool) []Person {
	query_string := "SELECT * FROM Participants WHERE name LIKE ?"
	switch filter_dep {
	case True:
		query_string += " AND boarded_departure=true"
	case False:
		query_string += " AND boarded_departure=false"
	}

	switch filter_ret {
	case True:
		query_string += " AND boarded_return=true"
	case False:
		query_string += " AND boarded_return=false"
	}

	switch is_staff {
	case True:
		query_string += " AND is_staff=true"
	case False:
		query_string += " AND is_staff=false"
	}

	if filter_coach != "" {
		query_string += " AND coach=?"
	}
	if filter_class != "" {
		query_string += " AND class=?"
	}

	query_string += " ORDER BY name;"

	// make search_name searchable
	search_name = fmt.Sprintf("%%%s%%", strings.ReplaceAll(search_name, "%", "\\%"))

	var rows *sql.Rows
	var err error

	if filter_coach != "" && filter_class != "" {
		rows, err = db.Query(query_string, search_name, filter_coach, filter_class)
	} else if filter_class != "" {
		rows, err = db.Query(query_string, search_name, filter_class)
	} else if filter_coach != "" {
		rows, err = db.Query(query_string, search_name, filter_coach)
	} else {
		rows, err = db.Query(query_string, search_name)
	}
	if err != nil {
		panic(err)
	}

	var people []Person

	for rows.Next() {
		var person Person
		err := rows.Scan(
			&person.Ticket_number,
			&person.Has_ep_ticket,
			&person.Boarded_dep,
			&person.Boarded_ret,
			&person.Name,
			&person.Phone_number,
			&person.Email,
			&person.Class,
			&person.Coach,
			&person.Is_staff,
		)
		if err != nil {
			panic(err)
		}

		people = append(people, person)
	}

	return people
}

func update_person(p Person) {
	db.Exec("UPDATE Participants SET has_ep_ticket=?, boarded_departure=?, boarded_return=?, name=?, phone_number=?, email=?, class=?, coach=?, is_staff=? WHERE ticket_number=?",
		p.Has_ep_ticket,
		p.Boarded_dep,
		p.Boarded_ret,
		p.Name,
		p.Phone_number,
		p.Email,
		p.Class,
		p.Coach,
		p.Is_staff,
		p.Ticket_number,
	)
}

func add_person(p Person) {
	_, err := db.Exec("INSERT INTO Participants (has_ep_ticket, boarded_departure, boarded_return, name, phone_number, email, class, coach, is_staff, ticket_number) VALUES (? ,?, ?, ?, ?, ?, ?, ?, ?, ?);",
		p.Has_ep_ticket,
		p.Boarded_dep,
		p.Boarded_ret,
		p.Name,
		p.Phone_number,
		p.Email,
		p.Class,
		p.Coach,
		p.Is_staff,
		p.Ticket_number,
	)
	if err != nil {
		panic(err)
	}
	//	for i := range reflect.ValueOf(p).NumField() {
	//		if reflect.ValueOf(p).Field(i).Kind() == reflect.Bool {
	//			fmt.Printf("%t,", reflect.ValueOf(p).Field(i).Bool())
	//		} else {
	//			print(reflect.ValueOf(p).Field(i).String(), ",")
	//		}
	//	}
	//
	// print("\n")
}

func validate(w http.ResponseWriter, r *http.Request) {
	if !check_authorised(r) {
		io.WriteString(w, "Unauthorised")
		return
	}

	given_ticket_number := r.FormValue("ticket_number")

	if !check_ticket_number(given_ticket_number) {
		t := template.Must(template.ParseFS(filesystem, "templates/invalid_ticket.html", "templates/reset_button.html"))
		t.Execute(w, nil)
		return
	}

	t, err := template.ParseFS(filesystem, "templates/valid_ticket.html", "templates/reset_button.html")
	if err != nil {
		log.Fatal(err)
	}

	template_input := Valid_template_entry{Person: get_person(given_ticket_number)}

	if r.FormValue("auto_board_disable") != "true" {
		if !template_input.Person.Boarded_dep {
			template_input.Person.Boarded_dep = true
			template_input.Dep_marked_automatically = true
		} else {
			template_input.Person.Boarded_ret = true
			template_input.Ret_marked_automatically = true
		}
		update_person(template_input.Person)
	}

	t.Execute(w, template_input)
}

func list(w http.ResponseWriter, r *http.Request) {
	if !check_authorised(r) {

		io.WriteString(w, "Unauthorised")
		return
	}

	list := People_list{
		People:        get_people("", None, None, "", "", None),
		List_complete: true,
		Only_data:     false,
		Coaches:       get_coaches(),
		Classes:       get_classes(),
	}

	t := template.Must(template.ParseFS(filesystem, "templates/people_list.html"))
	t.Execute(w, list)
}

func list_more(w http.ResponseWriter, r *http.Request) {
	if !check_authorised(r) {

		io.WriteString(w, "Unauthorised")
		return
	}
	list_search(w, r)
}

func list_search(w http.ResponseWriter, r *http.Request) {
	if !check_authorised(r) {

		io.WriteString(w, "Unauthorised")
		return
	}
	search_name := r.FormValue("search_name")

	filter_boarded_dep_str := r.FormValue("filter_dep")
	var filter_boarded_dep signed_bool
	switch filter_boarded_dep_str {
	case "true":
		filter_boarded_dep = True
	case "false":
		filter_boarded_dep = False
	default:
		filter_boarded_dep = None
	}

	filter_boarded_ret_str := r.FormValue("filter_ret")
	var filter_boarded_ret signed_bool
	switch filter_boarded_ret_str {
	case "true":
		filter_boarded_ret = True
	case "false":
		filter_boarded_ret = False
	default:
		filter_boarded_ret = None
	}

	is_staff_str := r.FormValue("is_staff")
	var is_staff signed_bool
	switch is_staff_str {
	case "true":
		is_staff = True
	case "false":
		is_staff = False
	default:
		is_staff = None
	}

	filter_coach := r.FormValue("coach")
	filter_class := r.FormValue("class")

	list := People_list{
		People:        get_people(search_name, filter_boarded_dep, filter_boarded_ret, filter_coach, filter_class, is_staff),
		List_complete: true,
		Only_data:     true,
		Coaches:       get_coaches(),
		Classes:       get_classes(),
	}

	t := template.Must(template.ParseFS(filesystem, "templates/people_list.html"))
	t.Execute(w, list)
}

func list_csv(w http.ResponseWriter, r *http.Request) {
	if !check_authorised(r) {

		io.WriteString(w, "Unauthorised")
		return
	}
	people := get_people("", None, None, "", "", None)

	people_csv_buffer := new(bytes.Buffer)
	writer := csv.NewWriter(people_csv_buffer)

	var fields []string
	for _, field := range reflect.VisibleFields(reflect.TypeOf(people[0])) {
		fields = append(fields, strings.ToLower(field.Name))
	}
	writer.Write(fields)

	for _, person := range people {
		reflect_person := reflect.ValueOf(person)
		row_person := make([]string, reflect_person.NumField())

		for i := range row_person {
			if reflect_person.Field(i).Type() == reflect.TypeOf(true) {
				row_person[i] = fmt.Sprintf("%t", reflect_person.Field(i).Bool())
			} else {
				row_person[i] = strings.TrimSpace(reflect_person.Field(i).String())
			}
		}
		err := writer.Write(row_person)
		if err != nil {
			panic(err)
		}
	}
	writer.Flush()
	err := writer.Error()
	if err != nil {
		panic(err)
	}

	people_csv_reader := io.NewSectionReader(bytes.NewReader(people_csv_buffer.Bytes()), 0, int64(people_csv_buffer.Len()))

	w.Header().Set("Content-Disposition", "attachment; filename="+fmt.Sprintf("data_%s.csv", time.Now().Format("2006-01-02-15-04-05")))
	w.Header().Set("Content-Type", r.Header.Get("Content-Type"))

	http.ServeContent(w, r, "people.csv", time.Now(), people_csv_reader)
}

func import_page(w http.ResponseWriter, r *http.Request) {
	if !check_authorised(r) {

		io.WriteString(w, "Unauthorised")
		return
	}

	t := template.Must(template.ParseFS(filesystem, "templates/import.html"))
	t.Execute(w, nil)
}

func import_csv(w http.ResponseWriter, r *http.Request) {
	if !check_authorised(r) {

		io.WriteString(w, "Unauthorised")
		return
	}

	csv_file, csv_header, err := r.FormFile("csv")
	if err != nil {
		invalid_csv(w, err.Error(), "/ticket_checker/ls/import")
		return
	}

	defer csv_file.Close()

	if !strings.HasSuffix(csv_header.Filename, ".csv") {
		invalid_csv(w, "given file is not a csv", "/ticket_checker/ls/import")
		return
	}

	csv_buffer := new(bytes.Buffer)

	read_section := make([]byte, 100)
	for {
		var read_section_len int
		read_section_len, err = csv_file.Read(read_section)
		if err != nil {
			if err != io.EOF {
				invalid_csv(w, err.Error(), "/ticket_checket/ls/import")
				return
			}
			break
		}
		csv_buffer.Write(read_section[:read_section_len])
	}

	reader := csv.NewReader(csv_buffer)

	csv_data, err := reader.ReadAll()
	if err != nil {
		invalid_csv(w, err.Error(), "/ticket_checker/ls/import")
		return
	}

	headers := csv_data[0]
	var csv_people []Person
	for i, row := range csv_data { // making sure data matches format
		if i == 0 {
			continue
		}
		// check if row is empty
		is_empty := true
		for _, column := range row {
			if column != "" {
				is_empty = false
			}
		}

		if is_empty {
			continue
		}

		csv_person := Person{}

		for ii, column := range row {
			switch headers[ii] {
			case "ticket_number":
				csv_person.Ticket_number = column
			case "has_ep_ticket":
				var value bool
				value, err = strconv.ParseBool(column)
				csv_person.Has_ep_ticket = value
			case "boarded_dep":
				var value bool
				value, err = strconv.ParseBool(column)
				csv_person.Boarded_dep = value
			case "boarded_ret":
				var value bool
				value, err = strconv.ParseBool(column)
				csv_person.Boarded_ret = value
			case "name":
				csv_person.Name = column
			case "phone_number":
				csv_person.Phone_number = column
			case "email":
				csv_person.Email = column
			case "class":
				csv_person.Class = column
			case "coach":
				csv_person.Coach = column
			case "is_staff":
				var value bool
				value, err = strconv.ParseBool(column)
				csv_person.Is_staff = value
			}

			if err != nil {
				invalid_csv(w, fmt.Sprintf("Error on line %d: %s", i, err.Error()), "/ticket_checker/ls/import")
				return
			}

		}
		// check for empty fields
		if csv_person.Name == "" {
			invalid_csv(w, fmt.Sprintf("Error on line %d: %s has no name", i, csv_person.Name), "/ticket_checker/ls/import")
		} else if csv_person.Ticket_number == "" {
			invalid_csv(w, fmt.Sprintf("Error on line %d: %s has no ticket_number", i, csv_person.Name), "/ticket_checker/ls/import")
		}

		csv_people = append(csv_people, csv_person)
	}

	_, err = db.Exec(
		`DROP TABLE IF EXISTS Participants;
		CREATE TABLE Participants (
			ticket_number TEXT,
			has_ep_ticket INTEGER,
			boarded_departure INTEGER,
			boarded_return INTEGER,
			name TEXT,
			phone_number TEXT,
			email TEXT,
			class TEXT,
			coach TEXT,
			is_staff INTEGER
		);
		CREATE INDEX Participants_index ON Participants(ticket_number, name, boarded_departure, boarded_return);
		`,
	)
	if err != nil {
		panic(err)
	}

	for _, person := range csv_people {
		add_person(person)
	}

	valid_csv(w, r, "/ticket_checker/")
}

func valid_csv(w http.ResponseWriter, r *http.Request, return_link string) {
	println("database_reset by CSV")
	t := template.Must(template.ParseFS(filesystem, "templates/valid_csv.html"))
	t.Execute(w, struct {
		Return string
	}{return_link})
}

func invalid_csv(w http.ResponseWriter, reason string, retry_link string) {
	println("invalid_csv")
	t := template.Must(template.ParseFS(filesystem, "templates/invalid_csv.html"))
	t.Execute(w, struct {
		Message string
		Retry   string
	}{reason, retry_link})
}

func main() {
	// Connect to DB
	var err error
	db, err = sql.Open("sqlite3", "./data/participants.db")
	if err != nil {
		log.Fatal(err)
	}
	println("Connected to DB")

	defer db.Close()

	// Setup server
	port, err := check_port(os.Getenv("PORT"))
	if err != nil {
		log.Fatal("Invalid port specified: ", err)
	}

	access_key = os.Getenv("ACCESS_KEY")
	println("Using access_key =", access_key)
	if access_key == "" {
		println("Note: set environment variable 'ACCESS_KEY' to set access_key")
	}

	// make endpoints
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/ticket_checker/", http.StatusSeeOther)
	})
	http.HandleFunc("/ticket_checker/", ticket_checker)
	http.HandleFunc("GET /ticket_checker/authorise/", authorise)
	http.HandleFunc("POST /ticket_checker/revert_boarding", revert_boarding)
	http.HandleFunc("POST /ticket_checker/authorise", check_access_key)
	http.HandleFunc("POST /ticket_checker/validate", validate)
	http.HandleFunc("POST /ticket_checker/ls", list)
	http.HandleFunc("POST /ticket_checker/ls/more", list_more)
	http.HandleFunc("POST /ticket_checker/ls/search", list_search)

	http.HandleFunc("GET /ticket_checker/ls/export", list_csv)
	http.HandleFunc("GET /ticket_checker/ls/import", import_page)
	http.HandleFunc("POST /ticket_checker/ls/import", import_csv)

	http.Handle("/assets/", http.StripPrefix("/assets/", http.FileServer(http.Dir("assets"))))

	fmt.Printf("Starting server at port 8080\n")
	err = http.ListenAndServe(fmt.Sprintf(":%s", port), nil)
	if err != nil {
		log.Fatal(err)
	}
}
