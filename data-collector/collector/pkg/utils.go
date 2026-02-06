package pkg

import "encoding/json"

/*
This function is meant to jsonify any object for inspect or whatever
*/
func Debug(val any) {
	data, _ := json.MarshalIndent(val, "", " ")
	println(string(data))
}
