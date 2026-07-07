// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package deals

import (
	"net/http"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

func (h Handlers) ListProducts(w http.ResponseWriter, r *http.Request, params crmcontracts.ListProductsParams) {
	in := ListProductsInput{
		Cursor:          params.Cursor,
		Limit:           params.Limit,
		Query:           params.Q,
		Active:          params.Active,
		IncludeArchived: params.IncludeArchived != nil && *params.IncludeArchived,
	}
	products, page, err := h.store.ListProducts(r.Context(), in)
	if err != nil {
		writeStoreErr(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, crmcontracts.ProductListResponse{Data: products, Page: pageInfo(page)})
}

func (h Handlers) CreateProduct(w http.ResponseWriter, r *http.Request, _ crmcontracts.CreateProductParams) {
	var req crmcontracts.CreateProductRequest
	if !httperr.Decode(w, r, &req) {
		return
	}
	if req.Name == "" {
		writeStoreErr(w, r, &RequiredFieldError{Field: "name"})
		return
	}
	in := CreateProductInput{
		Name:           req.Name,
		SKU:            req.Sku,
		Description:    req.Description,
		Unit:           req.Unit,
		UnitPriceMinor: req.UnitPriceMinor,
		Currency:       req.Currency,
		DefaultTaxRate: req.DefaultTaxRate,
		Active:         req.Active,
		Source:         req.Source,
	}
	product, err := h.store.CreateProduct(r.Context(), in)
	if err != nil {
		writeStoreErr(w, r, err)
		return
	}
	w.Header().Set("Location", "/v1/products/"+product.Id.String())
	httperr.WriteJSON(w, http.StatusCreated, product)
}

func (h Handlers) GetProduct(w http.ResponseWriter, r *http.Request, id crmcontracts.Id) {
	product, err := h.store.GetProduct(r.Context(), pathID[ids.ProductKind](id), storekit.IncludeArchived)
	if err != nil {
		writeStoreErr(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, product)
}

func (h Handlers) UpdateProduct(w http.ResponseWriter, r *http.Request, id crmcontracts.Id, _ crmcontracts.UpdateProductParams) {
	ifVersion, ok := httperr.IfMatchVersion(w, r)
	if !ok {
		return
	}
	var req crmcontracts.UpdateProductRequest
	if !httperr.Decode(w, r, &req) {
		return
	}
	in := UpdateProductInput{
		Name:           req.Name,
		SKU:            req.Sku,
		Description:    req.Description,
		Unit:           req.Unit,
		UnitPriceMinor: req.UnitPriceMinor,
		Currency:       req.Currency,
		DefaultTaxRate: req.DefaultTaxRate,
		Active:         req.Active,
		IfVersion:      ifVersion,
	}
	product, err := h.store.UpdateProduct(r.Context(), pathID[ids.ProductKind](id), in)
	if err != nil {
		writeStoreErr(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, product)
}

func (h Handlers) ArchiveProduct(w http.ResponseWriter, r *http.Request, id crmcontracts.Id) {
	product, err := h.store.ArchiveProduct(r.Context(), pathID[ids.ProductKind](id))
	if err != nil {
		writeStoreErr(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, product)
}
