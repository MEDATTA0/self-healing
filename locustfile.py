"""
Locust load test for self-healing infrastructure training
Generates varied traffic patterns to train the ML model
"""

from locust import HttpUser, task, between


class TrafficUser(HttpUser):
    """
    Simulates user traffic with varied patterns

    Run scenarios:
    1. Low traffic: locust -f locustfile.py --host http://localhost:3000 -u 5 -r 1
    2. Medium traffic: locust -f locustfile.py --host http://localhost:3000 -u 20 -r 5
    3. High traffic: locust -f locustfile.py --host http://localhost:3000 -u 50 -r 10
    4. Spike: locust -f locustfile.py --host http://localhost:3000 -u 100 -r 20
    """

    # Wait between 1-3 seconds between tasks
    wait_time = between(1, 3)

    @task(10)  # Weight: 10 (most common)
    def hello_endpoint(self):
        """Call the main hello endpoint"""
        with self.client.get("/hello", catch_response=True) as response:
            if response.status_code < 500:
                response.success()
            else:
                response.failure(f"Server error: {response.status_code}")

    @task(2)  # Weight: 2 (less common)
    def root_endpoint(self):
        """Call root endpoint if it exists"""
        with self.client.get("/", catch_response=True) as response:
            if response.status_code < 500:
                response.success()
            else:
                response.failure(f"Server error: {response.status_code}")
