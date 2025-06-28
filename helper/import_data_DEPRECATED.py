from os import name
import sqlite3
import csv

participant_template = {
    "id": None,
    "ticket_number": None,
    "has_ep_ticket": None,
    "boarding_status": None,
    "name": None,
    "phone_number": None,
    "email": None,
    "class": None,
    "coach": None,
}


def main():
    if input("Are you sure you want to reset the database? (y/n)") != "y":
        return

    c = sqlite3.connect("./data/participants.db")
    cur = c.cursor()

    cur.execute("DROP TABLE IF EXISTS Participants;")
    cur.execute("""
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
    """)
    cur.execute(
        "CREATE INDEX Participants_index ON Participants(ticket_number, name, boarded_departure, boarded_return)"
    )
    c.commit()

    with open("./data/data.csv", "r", newline="", encoding="utf-8-sig") as DATA:
        r = csv.DictReader(DATA)

        for row in r:
            row["name"] = row["name"].title()

            print(row)
            cur.execute(
                f"""
                        INSERT INTO Participants ({", ".join([key for key in row.keys()])}) 
                        VALUES ({", ".join(["?" for _ in row.keys()])})""",
                tuple(row.values()),
            )
            c.commit()
    c.close()


main()
