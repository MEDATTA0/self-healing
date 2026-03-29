# These are steps to use the community prometheus setup
# Make sure to have helm, kubectl installed
helm repo add prometheus-community https://prometheus-community.github.io/helm-charts
helm repo update

# Create the a namespace for the setup
kubectl create namespace monitoring

# Set up the prometheus environment
helm install prometheus prometheus-community/kube-prometheus-stack --namespace monitoring

# Verify if everything is fine
kubectl --namespace monitoring get pods
kubectl --namespace monitoring get svc

kubectl port-forward -n monitoring svc/prom-stack-kube-prometheus-prometheus 9090:9090