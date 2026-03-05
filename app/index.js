import express from "express";
import cors from "cors";

const PORT = 3000;
const app = express();

app.use(cors({ origin: "*" }));

// JSON logging middleware
app.use((req, res, next) => {
  const start = Date.now();
  
  res.on("finish", () => {
    const duration = Date.now() - start;
    const logEntry = {
      time: new Date().toISOString(),
      level: "info",
      msg: "Request completed",
      method: req.method,
      status_code: res.statusCode,
      path: req.url,
      user_agent: req.get("user-agent") || "",
      duration: `${duration}ms`,
    };
    console.log(JSON.stringify(logEntry));
  });
  
  next();
});

app.get("/hello", (req, res) => {
  let i = 1e4;
  while (i > 0) {
    i--;
  }

  return res.status(200).json({ message: "Hello World" });
});

app.get("/error", (_, res) => {
  let i = 1e3;
  while (i > 0) {
    i--;
  }
  return res
    .status(500)
    .json({ message: "Something went wrong, please try later" });
});

app.listen(PORT, () => {
  console.log(`server running on port: ${PORT}`);
});
