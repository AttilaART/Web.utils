package main

import (
	"bytes"
	"encoding/base64"
	"encoding/csv"
	"log"
	"os"
	"os/exec"
	"strings"
	"sync"
	"text/template"
	"time"

	"golang.org/x/text/cases"
	"golang.org/x/text/language"
)

var py_installed_tenp bool

type Ticket_info struct {
	Name          string
	Ticket_number string
	Coach_number  string
	Has_ep_ticket bool
	Date          string
	Year          string
}

func getProgramDir() string {
	command_str := ([]byte)(os.Args[0])
	return string(command_str[0 : len(command_str)-len("/make_ticket")])
}

func makeVenv() error {
	program_dir := getProgramDir()
	if program_dir == "" {
		program_dir = "./"
	}

	err := os.Mkdir(program_dir+"/python_modules", 0777)
	if err != nil {
		return err
	}

	out, err := exec.Command("python3", "-m", "venv", program_dir+"/python_modules").Output()
	if err != nil {
		return err
	}
	print(string(out))

	return nil
}

func installQr() (string, error) {
	program_dir := getProgramDir()
	if program_dir == "" {
		program_dir = "./"
	}

	out, err := exec.Command(program_dir+"/python_modules/bin/pip", "install", "qrcode[pil]").Output()
	if err != nil {
		return "", err
	}
	print(string(out))

	return program_dir + "/python_modules/bin/qr", nil
}

func installCairoSVG() (string, error) {
	program_dir := getProgramDir()
	out, err := exec.Command(program_dir+"/python_modules/bin/pip", "install", "cairosvg").Output()
	if err != nil {
		log.Panic(err)
	}
	print(string(out))

	return program_dir + "/python_modules/bin/cairosvg", nil
}

func make_ticket(person Ticket_info, qr_path string, cairo_path string, template *template.Template) {
	// make qr code in python
	cmd := exec.Command(qr_path, person.Ticket_number)

	var qr_code []byte
	qr_code, err := cmd.Output()
	if err != nil {
		panic(err)
	}

	// execute & write template to buffer
	svg := bytes.NewBuffer([]byte{})

	template.Execute(svg, struct {
		Ticket Ticket_info
		Img    string
	}{person, base64.StdEncoding.EncodeToString(qr_code)})

	ticket_path := os.Args[2] + "/" + strings.ReplaceAll(person.Name, " ", "_") + "." + person.Ticket_number
	err = os.RemoveAll(ticket_path + ".svg")
	if err != nil {
		log.Panic(err)
	}

	err = os.WriteFile(ticket_path+".svg", svg.Bytes(), 0444)
	if err != nil {
		log.Panic(err)
	}

	err = exec.Command(cairo_path, ticket_path+".svg", "-f", "pdf", "-o", ticket_path+".pdf").Run()
	if err != nil {
		log.Panic(err)
	}

	println("Made ticket: " + person.Name)
}

func main() {
	if len(os.Args) != 3 {
		log.Fatal("Usage: make_ticket [source.csv] [output_folder]")
	}

	program_dir := getProgramDir()
	if program_dir == "" {
		program_dir = "."
	}

	println(program_dir)

	qr_path := program_dir + "/python_modules/bin/qr"
	cairo_path := program_dir + "/python_modules/bin/cairosvg"
	// Check if dependencies are installed
	if _, err := os.Stat(program_dir + "/python_modules"); os.IsNotExist(err) {
		if err := makeVenv(); err != nil {
			log.Panic(err)
		}

		if qr_path, err = installQr(); err != nil {
			log.Panic(err)
		}

		if cairo_path, err = installCairoSVG(); err != nil {
			log.Panic(err)
		}

		py_installed_tenp = true
	}

	input_csv_path := os.Args[1]
	println(input_csv_path)

	input_csv, err := os.Open(input_csv_path)
	if err != nil {
		log.Panic(err)
	}
	defer input_csv.Close()

	reader := csv.NewReader(input_csv)
	data, err := reader.ReadAll()
	if err != nil {
		log.Panic(err)
	}

	err = os.Mkdir(os.Args[2], 0744)
	if err != nil {
		if strings.Contains(err.Error(), "file exists") {
		} else {
			log.Panic(err)
		}
	}

	column_labels := data[0]
	for column := range column_labels {
		column_labels[column] = strings.TrimSpace(column_labels[column])
		column_labels[column] = strings.ReplaceAll(column_labels[column], "\ufeff", "")
	}

	ticket_template := template.Must(template.ParseFiles(program_dir + "/templates/ticket_template.svg"))

	println("Reading People")

	var wg sync.WaitGroup

	var people []Ticket_info
	for i, row := range data {
		if i == 0 {
			continue
		}

		person := Ticket_info{}

		for column, column_data := range row {
			switch column_labels[column] {
			case "name", "Name":
				person.Name = cases.Title(language.German).String(column_data)
			case "Ticket_number", "ticket_number":
				person.Ticket_number = column_data
			case "Has_ep_ticket", "has_ep_ticket":
				if column_data == "1" {
					person.Has_ep_ticket = true
				} else {
					person.Has_ep_ticket = false
				}
			case "Coach", "coach":
				person.Coach_number = column_data
			}
		}

		if person.Coach_number == "" {
			person.Coach_number = "-1"
		}

		people = append(people, person)
	}
	println("Finished reding people")

	// split work between goroutines

	// make 10 threads
	thread_people := make([][]Ticket_info, 16)

	current_index := 0

	for current_index < len(people) {
		for i := range thread_people {
			if current_index >= len(people) {
				break
			}

			thread_people[i] = append(thread_people[i], people[current_index])
			current_index++
		}
	}

	start := time.Now()
	for i := range thread_people {
		wg.Add(1)
		go func(people_list []Ticket_info, wg *sync.WaitGroup) {
			for _, person := range people_list {
				println("Making Ticket: " + person.Name)
				make_ticket(person, qr_path, cairo_path, ticket_template)
			}
			wg.Done()
		}(thread_people[i], &wg)
	}
	wg.Wait()

	duration := time.Since(start)
	println("Finished!")
	println("Time elapesed: ", duration.String())

	if py_installed_tenp {
		program_dir := getProgramDir()

		err := os.RemoveAll(program_dir + "/python_modules")
		if err != nil {
			log.Panic(err)
		}
	}
}
