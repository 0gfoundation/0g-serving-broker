import { message } from "antd";
import { parseJSON } from "../../utils/json";

export interface ApiError {
  code: string;
  error: string;
}

export interface HttpQueryParams {
  [key: string]: string | number | boolean | undefined;
}

const handleResponse = (req: Request, res: Response) => {
  if (res.ok) {
    return res;
  }

  return res
    .clone()
    .text()
    .then((text: string) => {
      const err = parseJSON(text) as ApiError;
      if (!err) {
        throw res;
      }

      let msg = "";
      switch (res.status) {
        case 400:
          msg = err.error;
          break;
        default:
          break;
      }
      if (msg) {
        message.error(msg);
      }
      throw res;
    });
};

const authHeaders = (headersInit?: { [k: string]: string | null }) => {
  const headers = new Headers({
    Authorization: `Bearer ${localStorage.getItem("token")}`,
    Accept: "application/json",
    "Content-Type": "application/json",
  });

  Object.entries(headersInit || {}).forEach(([k, v]) => {
    if (v === null) {
      headers.delete(k);
    } else if (v) {
      headers.set(k, v);
    }
  });

  return headers;
};

const parseURL: (url: string, query?: HttpQueryParams) => string = (
  url,
  query
) => {
  const apiUrl = new URL(
    url,
    "http://localhost:1034" || window.location.origin
  );
  if (!query) {
    return apiUrl.toString();
  }

  const search = new URLSearchParams(apiUrl.search);
  Object.entries(query).forEach(([k, v]) => {
    if (v === null || v === undefined) {
      return;
    }
    search.set(k, v.toString());
  });
  apiUrl.search = search.toString();
  return apiUrl.toString();
};

const parseBody: (
  body?: { [key: string]: any } | FormData
) => string | FormData | null = (body) => {
  if (!body) {
    return null;
  }
  if (body instanceof FormData) {
    return body;
  }
  return JSON.stringify(body);
};

const sendRequest = (req: Request) =>
  fetch(req).then((res) => handleResponse(req, res));

export const get = (
  url: string,
  options?: {
    headers?: { [key: string]: string | null };
    params?: HttpQueryParams;
  }
) =>
  sendRequest(
    new Request(parseURL(url, options?.params), {
      headers: authHeaders(options?.headers),
    })
  );

export const post = (
  url: string,
  options?: {
    headers?: { [key: string]: string | null };
    body?: { [key: string]: any } | FormData;
    params?: HttpQueryParams;
  }
) =>
  sendRequest(
    new Request(parseURL(url, options?.params), {
      headers: authHeaders(options?.headers),
      method: "POST",
      body: parseBody(options?.body),
    })
  );

export const put = (
  url: string,
  options?: {
    headers?: { [key: string]: string | null };
    body?: { [key: string]: any };
    params?: HttpQueryParams;
  }
) =>
  sendRequest(
    new Request(parseURL(url, options?.params), {
      headers: authHeaders(options?.headers),
      method: "PUT",
      body: parseBody(options?.body),
    })
  );

export const del = (
  url: string,
  options?: {
    headers?: { [key: string]: string | null };
    params?: HttpQueryParams;
  }
) =>
  sendRequest(
    new Request(parseURL(url, options?.params), {
      headers: authHeaders(options?.headers),
      method: "DELETE",
    })
  );
