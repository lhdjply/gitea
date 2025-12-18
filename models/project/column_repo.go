// Copyright 2025 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package project

import (
	"context"

	"code.gitea.io/gitea/models/db"
	repo_model "code.gitea.io/gitea/models/repo"
	"code.gitea.io/gitea/modules/log"
)

type ColumnRepo struct {
	ID       int64 `xorm:"pk autoincr"`
	ColumnID int64 `xorm:"UNIQUE(s) INDEX NOT NULL"`
	RepoID   int64 `xorm:"UNIQUE(s) INDEX NOT NULL"`
	Sorting  int64 `xorm:"NOT NULL DEFAULT 0"`
}

func (ColumnRepo) TableName() string {
	return "project_board_repo"
}

func init() {
	db.RegisterModel(new(ColumnRepo))
}

func AddRepoToColumn(ctx context.Context, columnID, repoID int64) error {
	column, err := GetColumn(ctx, columnID)
	if err != nil {
		return err
	}

	project, err := GetProjectByID(ctx, column.ProjectID)
	if err != nil {
		return err
	}

	if !project.IsOrganizationProject() {
		return ErrCannotBindRepoToRepoProject{}
	}

	var maxSorting int64
	_, err = db.GetEngine(ctx).
		Table("project_board_repo").
		Where("column_id = ?", columnID).
		Select("MAX(sorting)").
		Get(&maxSorting)
	if err != nil {
		return err
	}

	columnRepo := &ColumnRepo{
		ColumnID: columnID,
		RepoID:   repoID,
		Sorting:  maxSorting + 1,
	}

	return db.Insert(ctx, columnRepo)
}

func RemoveRepoFromColumn(ctx context.Context, columnID, repoID int64) error {
	_, err := db.GetEngine(ctx).Where("column_id = ? AND repo_id = ?", columnID, repoID).Delete(&ColumnRepo{})
	return err
}

type RepoWithSorting struct {
	Repo    *repo_model.Repository
	Sorting int64
}

func GetColumnReposByColumnID(ctx context.Context, columnID int64) ([]*repo_model.Repository, error) {
	repos := make([]*repo_model.Repository, 0, 5)
	err := db.GetEngine(ctx).
		Join("INNER", "project_board_repo", "repository.id = project_board_repo.repo_id").
		Where("project_board_repo.column_id = ?", columnID).
		OrderBy("project_board_repo.sorting ASC, project_board_repo.id ASC").
		Find(&repos)
	if err == nil && len(repos) > 0 {
		log.Info("GetColumnReposByColumnID: columnID=%d, found %d repos", columnID, len(repos))
	}
	return repos, err
}

func GetColumnReposWithSorting(ctx context.Context, columnID int64) ([]*RepoWithSorting, error) {
	type repoSorting struct {
		repo_model.Repository `xorm:"extends"`
		Sorting               int64
	}
	
	results := make([]*repoSorting, 0, 5)
	err := db.GetEngine(ctx).
		Table("repository").
		Select("repository.*, project_board_repo.sorting").
		Join("INNER", "project_board_repo", "repository.id = project_board_repo.repo_id").
		Where("project_board_repo.column_id = ?", columnID).
		OrderBy("project_board_repo.sorting ASC, project_board_repo.id ASC").
		Find(&results)
	
	reposWithSorting := make([]*RepoWithSorting, 0, len(results))
	for _, r := range results {
		reposWithSorting = append(reposWithSorting, &RepoWithSorting{
			Repo:    &r.Repository,
			Sorting: r.Sorting,
		})
	}
	
	return reposWithSorting, err
}

func GetColumnIDsByRepoID(ctx context.Context, repoID int64) ([]int64, error) {
	columnIDs := make([]int64, 0, 5)
	return columnIDs, db.GetEngine(ctx).
		Table("project_board_repo").
		Where("repo_id = ?", repoID).
		Cols("column_id").
		Find(&columnIDs)
}

func UpdateColumnRepoSorting(ctx context.Context, columnID, repoID, sorting int64) error {
	result, err := db.GetEngine(ctx).
		Where("column_id = ? AND repo_id = ?", columnID, repoID).
		Cols("sorting").
		Update(&ColumnRepo{Sorting: sorting})
	if err == nil {
		log.Info("UpdateColumnRepoSorting: columnID=%d, repoID=%d, sorting=%d, affected=%d", columnID, repoID, sorting, result)
	}
	return err
}

type ErrCannotBindRepoToRepoProject struct{}

func (err ErrCannotBindRepoToRepoProject) Error() string {
	return "cannot bind repository to repository project"
}

func IsErrCannotBindRepoToRepoProject(err error) bool {
	_, ok := err.(ErrCannotBindRepoToRepoProject)
	return ok
}
