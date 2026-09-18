//go:build !with_public_sub

package api

const HasPublicSubFeature = false

type publicSubController struct{}

func newPublicSubController() *publicSubController {
	return &publicSubController{}
}

func (p *publicSubController) Start(s *Server, port int) {
	// Подписка отключена в данной сборке
}

func (p *publicSubController) Stop() {
	// Подписка отключена в данной сборке
}
