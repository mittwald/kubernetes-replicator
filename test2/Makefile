.PHONY: test clean

test:
	kubectl create namespace test

	kubectl apply -f secret1.yaml -n test
	kubectl apply -f secret2.yaml -n test
	kubectl apply -f secret3.yaml -n test

	kubectl apply -f configmap1.yaml -n test
	kubectl apply -f configmap2.yaml -n test
	kubectl apply -f configmap3.yaml -n test

clean:
	kubectl delete namespace test
