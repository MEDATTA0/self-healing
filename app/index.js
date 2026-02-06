import express from "express";
import morgan from "morgan";
import cors from "cors";

const PORT = 3000;
const app = express();

app.use(cors({ origin: "*" }));
app.use(morgan("combined"));

app.get("/hello", (req, res) => {
  let i = 1e4;
  while (i > 0) {
    i--;
  }

  return res.status(200).json({ message: "Hello World" });
});

app.listen(PORT, () => {
  console.log(`server running on port: ${PORT}`);
});
