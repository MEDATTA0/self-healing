FROM node:24-alpine

RUN apk update

WORKDIR /app

COPY ./ ./

RUN yarn install

EXPOSE 3000

CMD [ "yarn", "start" ]