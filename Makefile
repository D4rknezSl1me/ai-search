# ai-search task runner (make). Windows users: use .\tasks.ps1 instead.
COMPOSE = docker compose --env-file .env -f deploy/docker-compose.yml

.PHONY: init-env up up-app up-gpu down ps logs health

init-env:
	@test -f .env || cp .env.example .env

up: init-env
	$(COMPOSE) up -d

up-app: init-env
	$(COMPOSE) --profile app up -d --build

up-gpu: init-env
	$(COMPOSE) --profile gpu up -d

down:
	$(COMPOSE) down

ps:
	$(COMPOSE) ps

logs:
	$(COMPOSE) logs -f --tail=100

health:
	@curl -s localhost:9200/_cluster/health; echo
	@curl -s localhost:6333/readyz; echo
	@curl -s localhost:8222/healthz; echo
